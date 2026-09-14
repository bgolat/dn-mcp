// Package dnapi is a thin client for the Defined Networking admin API.
//
// It deliberately does not model response bodies: responses are passed through
// to the caller as raw JSON. Only request shapes are typed, because only those
// need a JSON schema for MCP tool inputs. This keeps the drift surface between
// this client and the API small.
package dnapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL is the production Defined Networking API.
const DefaultBaseURL = "https://api.defined.net"

// Client talks to the Defined Networking admin API using an API key.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// New returns a Client for baseURL authenticating with apiKey. If baseURL is
// empty, DefaultBaseURL is used.
func New(baseURL, apiKey string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		apiKey:  apiKey,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// APIError is a non-2xx response from the API. The API returns a list of
// errors; Message concatenates them so the text reaches the model intact,
// since these messages are what let an agent correct a bad request.
type APIError struct {
	StatusCode int
	Errors     []APIErrorDetail
	Raw        string
}

// APIErrorDetail mirrors one entry of the API's {"errors":[...]} envelope.
type APIErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Path    []any  `json:"path,omitempty"`
	Param   string `json:"param,omitempty"`
}

func (e *APIError) Error() string {
	if len(e.Errors) == 0 {
		return fmt.Sprintf("API returned HTTP %d: %s", e.StatusCode, e.Raw)
	}
	parts := make([]string, 0, len(e.Errors))
	for _, d := range e.Errors {
		p := d.Message
		if p == "" {
			p = d.Code
		}
		if len(d.Path) > 0 {
			segs := make([]string, len(d.Path))
			for i, s := range d.Path {
				segs[i] = fmt.Sprint(s)
			}
			p = fmt.Sprintf("%s (field: %s)", p, strings.Join(segs, "."))
		}
		parts = append(parts, p)
	}
	return fmt.Sprintf("API returned HTTP %d: %s", e.StatusCode, strings.Join(parts, "; "))
}

// Do performs an authenticated request and returns the "data" field of the
// response envelope. body may be nil.
func (c *Client) Do(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	env, err := c.doEnvelope(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	return env.Data, nil
}

// DoPaged is like Do but also returns the response metadata, which carries
// pagination cursors for list endpoints.
func (c *Client) DoPaged(ctx context.Context, method, path string, body any) (json.RawMessage, json.RawMessage, error) {
	env, err := c.doEnvelope(ctx, method, path, body)
	if err != nil {
		return nil, nil, err
	}
	return env.Data, env.Metadata, nil
}

type envelope struct {
	Data     json.RawMessage `json:"data"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

func (c *Client) doEnvelope(ctx context.Context, method, path string, body any) (*envelope, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to encode request body: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, fmt.Errorf("failed to build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request to %s failed: %w", path, err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		apiErr := &APIError{StatusCode: res.StatusCode, Raw: string(raw)}
		var errEnv struct {
			Errors []APIErrorDetail `json:"errors"`
		}
		if err := json.Unmarshal(raw, &errEnv); err == nil {
			apiErr.Errors = errEnv.Errors
		}
		return nil, apiErr
	}

	// 204 and other empty successes have no envelope to decode.
	if len(bytes.TrimSpace(raw)) == 0 {
		return &envelope{Data: json.RawMessage("null")}, nil
	}

	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("failed to decode response from %s: %w", path, err)
	}
	return &env, nil
}

// Query builds a query string from non-empty values.
func Query(pairs map[string]string) string {
	values := url.Values{}
	for k, v := range pairs {
		if v != "" {
			values.Set(k, v)
		}
	}
	if len(values) == 0 {
		return ""
	}
	return "?" + values.Encode()
}

// StreamTimeoutSlack is how much longer than the server's own deadline we wait
// before giving up. The API allows a command its duration plus 30s, so this
// keeps the client from timing out first and reporting a false failure.
const StreamTimeoutSlack = 45 * time.Second

// Stream issues a request whose response is a stream of newline-delimited JSON
// envelopes, and collects every message until the stream ends.
//
// The dnclient command endpoint writes its 200 header only once the host's
// first message arrives. If the host never answers, or goes away mid-stream,
// the API reports that as an error object — before the header as a non-2xx
// response, or after it as an {"errors":[...]} line inside an otherwise
// successful body. Both are surfaced here as an error.
func (c *Client) Stream(ctx context.Context, method, path string, body any, timeout time.Duration) ([]json.RawMessage, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to encode request body: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, fmt.Errorf("failed to build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/x-ndjson")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	// The shared client's fixed timeout is too short for streaming commands, so
	// this request is bounded by the context instead.
	res, err := (&http.Client{}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("request to %s failed: %w", path, err)
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		raw, _ := io.ReadAll(res.Body)
		apiErr := &APIError{StatusCode: res.StatusCode, Raw: string(raw)}
		var errEnv struct {
			Errors []APIErrorDetail `json:"errors"`
		}
		if err := json.Unmarshal(raw, &errEnv); err == nil {
			apiErr.Errors = errEnv.Errors
		}
		return nil, apiErr
	}

	var messages []json.RawMessage
	scanner := bufio.NewScanner(res.Body)
	// A DebugStack dump is far larger than the default 64KiB line limit.
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var env struct {
			Data   json.RawMessage  `json:"data"`
			Errors []APIErrorDetail `json:"errors"`
		}
		if err := json.Unmarshal(line, &env); err != nil {
			// Not an envelope; keep the raw line rather than dropping output.
			messages = append(messages, json.RawMessage(bytes.Clone(line)))
			continue
		}
		// An error written into an already-200 body means the host went away
		// partway through. Report it, but keep whatever arrived first.
		if len(env.Errors) > 0 {
			return messages, &APIError{StatusCode: res.StatusCode, Errors: env.Errors, Raw: string(line)}
		}
		if len(env.Data) > 0 {
			messages = append(messages, bytes.Clone(env.Data))
		}
	}
	if err := scanner.Err(); err != nil {
		// A timeout here means the stream ran its full course, which is normal
		// for StreamLogs; return what we collected rather than failing.
		if ctx.Err() != nil && len(messages) > 0 {
			return messages, nil
		}
		return messages, fmt.Errorf("failed reading stream from %s: %w", path, err)
	}
	return messages, nil
}

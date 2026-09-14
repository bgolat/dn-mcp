package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bgolat/dn-mcp/internal/dnapi"
)

// ndjsonServer streams the given lines as the command endpoint does, and
// records the request body it was sent.
func ndjsonServer(t *testing.T, gotBody *map[string]any, status int, lines ...string) *dnapi.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if gotBody != nil {
			json.Unmarshal(body, gotBody)
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(status)
		for _, l := range lines {
			io.WriteString(w, l+"\n")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}))
	t.Cleanup(srv.Close)
	return dnapi.New(srv.URL, "test-key")
}

func resultText(t *testing.T, res any) string {
	t.Helper()
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("failed to marshal result: %v", err)
	}
	return string(b)
}

func TestDiagnosticSendsCorrectCommand(t *testing.T) {
	var body map[string]any
	c := ndjsonServer(t, &body, 200, `{"data":{"commands":["Ping","StreamLogs"]}}`)

	r, _, err := runDiagnostic(context.Background(), c, diagnosticArgs{
		HostID: "host-1", Command: "ListCommands",
	})
	if err != nil {
		t.Fatalf("runDiagnostic failed: %v", err)
	}
	if body["command"] != "ListCommands" {
		t.Errorf("command = %v, want ListCommands", body["command"])
	}
	out := resultText(t, r.Content[0])
	if !strings.Contains(out, "StreamLogs") {
		t.Errorf("result did not carry the client's output: %s", out)
	}
}

// A target-taking command must pass the target through, and must refuse
// without one rather than sending a malformed request.
func TestDiagnosticTargetHandling(t *testing.T) {
	var body map[string]any
	c := ndjsonServer(t, &body, 200, `{"data":{"tunnel":"established"}}`)

	if _, _, err := runDiagnostic(context.Background(), c, diagnosticArgs{
		HostID: "host-1", Command: "QueryLighthouse", Target: "fdef::1",
	}); err != nil {
		t.Fatalf("runDiagnostic failed: %v", err)
	}
	args, _ := body["args"].(map[string]any)
	if args["target"] != "fdef::1" {
		t.Errorf("target = %v, want fdef::1", args["target"])
	}

	_, _, err := runDiagnostic(context.Background(), c, diagnosticArgs{
		HostID: "host-1", Command: "QueryLighthouse",
	})
	if err == nil {
		t.Fatal("expected an error when target is missing")
	}
	if !strings.Contains(err.Error(), "target") {
		t.Errorf("error should name the missing target, got: %v", err)
	}
}

// Restart must not be reachable through the diagnostic tool; it has its own.
func TestDiagnosticRejectsRestart(t *testing.T) {
	c := ndjsonServer(t, nil, 200, `{"data":{}}`)
	_, _, err := runDiagnostic(context.Background(), c, diagnosticArgs{
		HostID: "host-1", Command: "Restart",
	})
	if err == nil {
		t.Fatal("expected Restart to be rejected by run_host_diagnostic")
	}
	if !strings.Contains(err.Error(), "restart_host_client") {
		t.Errorf("error should point at the dedicated tool, got: %v", err)
	}
}

func TestStreamLogsCollectsMultipleLines(t *testing.T) {
	var body map[string]any
	c := ndjsonServer(t, &body, 200,
		`{"data":{"msg":"line one"}}`,
		`{"data":{"msg":"line two"}}`,
		`{"data":{"msg":"line three"}}`,
	)

	r, _, err := streamLogs(context.Background(), c, streamLogsArgs{
		HostID: "host-1", DurationSeconds: 5, Level: "debug",
	})
	if err != nil {
		t.Fatalf("streamLogs failed: %v", err)
	}
	args, _ := body["args"].(map[string]any)
	if args["durationSeconds"] != float64(5) || args["level"] != "debug" {
		t.Errorf("args = %v, want durationSeconds=5 level=debug", args)
	}
	out := resultText(t, r.Content[0])
	for _, want := range []string{"line one", "line two", "line three"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q: %s", want, out)
		}
	}
}

func TestStreamLogsDefaultsAndValidation(t *testing.T) {
	var body map[string]any
	c := ndjsonServer(t, &body, 200, `{"data":{"msg":"x"}}`)

	if _, _, err := streamLogs(context.Background(), c, streamLogsArgs{HostID: "host-1"}); err != nil {
		t.Fatalf("streamLogs with defaults failed: %v", err)
	}
	args, _ := body["args"].(map[string]any)
	if args["durationSeconds"] != float64(15) || args["level"] != "info" {
		t.Errorf("defaults = %v, want durationSeconds=15 level=info", args)
	}

	if _, _, err := streamLogs(context.Background(), c, streamLogsArgs{
		HostID: "host-1", DurationSeconds: 601,
	}); err == nil {
		t.Error("expected an error for durationSeconds above 600")
	}
	if _, _, err := streamLogs(context.Background(), c, streamLogsArgs{
		HostID: "host-1", Level: "verbose",
	}); err == nil {
		t.Error("expected an error for an unknown log level")
	}
}

// The API writes its 200 header on the first message, so a host that goes away
// mid-stream produces an error object inside an otherwise successful body.
// The lines that did arrive are still worth returning.
func TestStreamLogsKeepsPartialOutputOnMidStreamError(t *testing.T) {
	c := ndjsonServer(t, nil, 200,
		`{"data":{"msg":"before the host went away"}}`,
		`{"errors":[{"code":"ERR_DNCLIENT_DISCONNECTED","message":"Host has gone away"}]}`,
	)

	r, _, err := streamLogs(context.Background(), c, streamLogsArgs{HostID: "host-1"})
	if err != nil {
		t.Fatalf("expected partial output rather than a hard failure, got: %v", err)
	}
	out := resultText(t, r.Content[0])
	if !strings.Contains(out, "before the host went away") {
		t.Errorf("partial output was lost: %s", out)
	}
	if !strings.Contains(out, "gone away") {
		t.Errorf("the disconnect reason should be reported: %s", out)
	}
}

// An offline host fails before any header is written, as a normal HTTP error.
func TestDiagnosticSurfacesOfflineHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		io.WriteString(w, `{"errors":[{"code":"ERR_DNCLIENT_NOT_REACHABLE","message":"dnclient is not reachable"}]}`)
	}))
	defer srv.Close()

	_, _, err := runDiagnostic(context.Background(), dnapi.New(srv.URL, "k"),
		diagnosticArgs{HostID: "host-1", Command: "Ping"})
	if err == nil {
		t.Fatal("expected an error for an unreachable client")
	}
	if !strings.Contains(err.Error(), "not reachable") {
		t.Errorf("error should explain the host is unreachable, got: %v", err)
	}
}

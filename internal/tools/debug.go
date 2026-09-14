package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/bgolat/dn-mcp/internal/dnapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Commands the API accepts on POST /v1/hosts/{hostID}/command. Restart is
// deliberately absent from the diagnostic set — it is disruptive, so it gets
// its own tool rather than riding in as one enum value among many.
var diagnosticCommands = map[string]struct {
	needsTarget bool
	summary     string
}{
	"Ping":            {false, "check that the client is online and answering"},
	"ListCommands":    {false, "list the commands this client's version supports"},
	"PrintCert":       {true, "print a certificate — the host's own, or the target's as this host sees it"},
	"PrintTunnel":     {true, "print the state of the existing tunnel to the target"},
	"QueryLighthouse": {true, "ask the lighthouse where the target is, to diagnose discovery"},
	"CreateTunnel":    {true, "attempt to open a tunnel to the target, to test reachability"},
	"DebugStack":      {false, "dump the client's goroutine stacks"},
}

// oneShotTimeout covers the API's 30s deadline for non-streaming commands plus
// slack, so the client does not give up before the server does.
const oneShotTimeout = dnapi.StreamTimeoutSlack

type diagnosticArgs struct {
	HostID  string `json:"hostID" jsonschema:"ID of the host to run the command on."`
	Command string `json:"command" jsonschema:"One of: Ping, ListCommands, PrintCert, PrintTunnel, QueryLighthouse, CreateTunnel, DebugStack."`
	Target  string `json:"target,omitempty" jsonschema:"Nebula IP address of the other host, for PrintCert, PrintTunnel, QueryLighthouse and CreateTunnel. Get it from list_hosts ipAddresses."`
}

type streamLogsArgs struct {
	HostID          string `json:"hostID" jsonschema:"ID of the host to stream logs from."`
	DurationSeconds int    `json:"durationSeconds,omitempty" jsonschema:"How long to collect logs, 1-600 seconds. Defaults to 15. The call blocks for this long, so prefer a short window."`
	Level           string `json:"level,omitempty" jsonschema:"Minimum log level: panic, fatal, error, warning, info, or debug. Defaults to info."`
}

type restartArgs struct {
	HostID string `json:"hostID" jsonschema:"ID of the host whose client should be restarted."`
}

func registerDebugTools(s *mcp.Server, c *dnapi.Client) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "run_host_diagnostic",
		Description: "Run a read-only diagnostic command on a host's dnclient and return its output. " +
			"Commands: Ping (is the client online), ListCommands (what this client version supports), " +
			"PrintCert (certificate details), QueryLighthouse (where the lighthouse thinks a target is), " +
			"CreateTunnel (try to reach a target), PrintTunnel (state of an existing tunnel), DebugStack (goroutine dump). " +
			"PrintCert, PrintTunnel, QueryLighthouse and CreateTunnel need a 'target' Nebula IP. " +
			"The host must be online: these reach the running client, so a host that is offline returns a not-reachable error.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args diagnosticArgs) (*mcp.CallToolResult, any, error) {
		return runDiagnostic(ctx, c, args)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "stream_host_logs",
		Description: "Collect live logs from a host's dnclient for a fixed window and return them. " +
			"This blocks for the whole window, so keep it short — 15 seconds is usually enough to catch a recurring problem. " +
			"Logs are only produced while the window is open, so trigger the behaviour you are debugging during it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args streamLogsArgs) (*mcp.CallToolResult, any, error) {
		return streamLogs(ctx, c, args)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "restart_host_client",
		Description: "Restart the dnclient on a host. This briefly drops the host's tunnels and network access while it comes back up. " +
			"It does not change any configuration. Confirm the exact host with the user before calling this.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args restartArgs) (*mcp.CallToolResult, any, error) {
		msgs, err := c.Stream(ctx, "POST", commandPath(args.HostID),
			map[string]any{"command": "Restart", "args": map[string]any{}}, oneShotTimeout)
		if err != nil {
			return nil, nil, err
		}
		return commandResult("Restart", msgs)
	})
}

func commandPath(hostID string) string {
	return "/v1/hosts/" + url.PathEscape(hostID) + "/command"
}

func runDiagnostic(ctx context.Context, c *dnapi.Client, args diagnosticArgs) (*mcp.CallToolResult, any, error) {
	spec, ok := diagnosticCommands[args.Command]
	if !ok {
		valid := make([]string, 0, len(diagnosticCommands))
		for k := range diagnosticCommands {
			valid = append(valid, k)
		}
		return nil, nil, fmt.Errorf("unknown command %q; valid commands are %s (to restart a client, use restart_host_client)",
			args.Command, strings.Join(valid, ", "))
	}

	cmdArgs := map[string]any{}
	if spec.needsTarget {
		if args.Target == "" {
			return nil, nil, fmt.Errorf("command %s requires a 'target' Nebula IP address (%s)", args.Command, spec.summary)
		}
		cmdArgs["target"] = args.Target
	}

	msgs, err := c.Stream(ctx, "POST", commandPath(args.HostID),
		map[string]any{"command": args.Command, "args": cmdArgs}, oneShotTimeout)
	if err != nil {
		return nil, nil, err
	}
	return commandResult(args.Command, msgs)
}

func streamLogs(ctx context.Context, c *dnapi.Client, args streamLogsArgs) (*mcp.CallToolResult, any, error) {
	duration := args.DurationSeconds
	if duration == 0 {
		duration = 15
	}
	if duration < 1 || duration > 600 {
		return nil, nil, fmt.Errorf("durationSeconds must be between 1 and 600, got %d", duration)
	}

	level := strings.ToLower(args.Level)
	if level == "" {
		level = "info"
	}
	switch level {
	case "panic", "fatal", "error", "warning", "info", "debug":
	default:
		return nil, nil, fmt.Errorf("unknown log level %q; valid levels are panic, fatal, error, warning, info, debug", args.Level)
	}

	// The API allows the command its full duration plus 30s before giving up.
	timeout := time.Duration(duration)*time.Second + dnapi.StreamTimeoutSlack

	msgs, err := c.Stream(ctx, "POST", commandPath(args.HostID), map[string]any{
		"command": "StreamLogs",
		"args":    map[string]any{"durationSeconds": duration, "level": level},
	}, timeout)
	if err != nil {
		// Partial output is still useful when the host drops mid-stream.
		if len(msgs) > 0 {
			out, _ := renderMessages(msgs)
			return jsonResult(map[string]any{
				"command":       "StreamLogs",
				"error":         err.Error(),
				"partialOutput": out,
				"lineCount":     len(msgs),
			})
		}
		return nil, nil, err
	}
	out, _ := renderMessages(msgs)
	return jsonResult(map[string]any{
		"command":         "StreamLogs",
		"durationSeconds": duration,
		"level":           level,
		"lineCount":       len(msgs),
		"output":          out,
	})
}

// commandResult renders a command's collected messages. A single message is
// unwrapped so the common one-shot case reads as a plain object.
func commandResult(command string, msgs []json.RawMessage) (*mcp.CallToolResult, any, error) {
	out, single := renderMessages(msgs)
	result := map[string]any{"command": command}
	if len(msgs) == 0 {
		result["output"] = nil
		result["note"] = "the client returned no output"
	} else if single != nil {
		result["output"] = single
	} else {
		result["output"] = out
		result["lineCount"] = len(msgs)
	}
	return jsonResult(result)
}

// renderMessages decodes collected messages. It returns the list form always,
// and additionally the lone decoded value when there was exactly one message.
func renderMessages(msgs []json.RawMessage) ([]any, any) {
	out := make([]any, 0, len(msgs))
	for _, m := range msgs {
		var v any
		if err := json.Unmarshal(m, &v); err != nil {
			out = append(out, string(m))
			continue
		}
		out = append(out, v)
	}
	if len(out) == 1 {
		return out, out[0]
	}
	return out, nil
}

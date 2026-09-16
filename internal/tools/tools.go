// Package tools registers the Defined Networking MCP tool set.
//
// Hosts, roles and tags are writable; networks and routes are read-only.
// Role and tag writes edit firewall rules, which grant network access, so they
// are the escalating part of this tool set. networks:delete is unrecoverable
// and stays excluded. Pair this server with an API key scoped to what a given
// agent needs — for example without roles:* and tags:* write permissions — so
// the boundary is enforced server-side, not just by which tools exist here.
package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/bgolat/dn-mcp/internal/dnapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Register adds every tool to the server.
func Register(s *mcp.Server, c *dnapi.Client) {
	registerReadTools(s, c)
	registerHostTools(s, c)
	registerDebugTools(s, c)
	registerPolicyTools(s, c)
}

// jsonResult renders v as indented JSON in a tool result.
func jsonResult(v any) (*mcp.CallToolResult, any, error) {
	encoded, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("failed to encode tool result: %w", err)
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(encoded)}},
	}, nil, nil
}

// rawResult renders already-encoded JSON in a tool result, re-indenting it so
// the model reads it more reliably.
func rawResult(raw json.RawMessage) (*mcp.CallToolResult, any, error) {
	var pretty any
	if err := json.Unmarshal(raw, &pretty); err != nil {
		// Not valid JSON: pass it through rather than failing the call.
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}},
		}, nil, nil
	}
	return jsonResult(pretty)
}

// listResult renders a list page together with its pagination metadata, so the
// model can tell a truncated page from a complete one.
func listResult(data, metadata json.RawMessage) (*mcp.CallToolResult, any, error) {
	var items, meta any
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, nil, fmt.Errorf("failed to decode list response: %w", err)
	}
	out := map[string]any{"items": items}
	if len(metadata) > 0 {
		if err := json.Unmarshal(metadata, &meta); err == nil {
			out["pagination"] = meta
		}
	}
	return jsonResult(out)
}

// get is a small helper for read-only endpoints returning a single object.
func get(ctx context.Context, c *dnapi.Client, path string) (*mcp.CallToolResult, any, error) {
	raw, err := c.Do(ctx, "GET", path, nil)
	if err != nil {
		return nil, nil, err
	}
	return rawResult(raw)
}

// list is a small helper for read-only endpoints returning a page of objects.
func list(ctx context.Context, c *dnapi.Client, path string) (*mcp.CallToolResult, any, error) {
	data, meta, err := c.DoPaged(ctx, "GET", path, nil)
	if err != nil {
		return nil, nil, err
	}
	return listResult(data, meta)
}

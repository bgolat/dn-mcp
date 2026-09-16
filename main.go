// Command dn-mcp is an MCP server for managing a Defined Networking (Nebula)
// network from an AI agent such as Claude Code.
//
// It exposes the Defined Networking admin API as MCP tools over stdio. Hosts,
// roles and tags are writable, including role and tag firewall rules; networks
// and routes are read-only. Scope the API key to what an agent actually needs so
// the boundary is enforced by the server too, not only by which tools exist.
//
// Configuration is by environment:
//
//	DN_API_KEY  (required) a Defined Networking API key
//	DN_API_URL  (optional) API base URL, defaults to https://api.defined.net
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/bgolat/dn-mcp/internal/dnapi"
	"github.com/bgolat/dn-mcp/internal/tools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	// stdout is the JSON-RPC channel for the stdio transport, so every log
	// line must go to stderr or it will corrupt the protocol stream.
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	if err := run(log); err != nil {
		log.Error("server failed", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	apiKey := os.Getenv("DN_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("DN_API_KEY is not set; create an API key in the Defined Networking admin UI and export it")
	}

	client := dnapi.New(os.Getenv("DN_API_URL"), apiKey)

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "defined-networking",
		Version: version,
	}, nil)
	tools.Register(server, client)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Info("starting Defined Networking MCP server", "version", version)
	return server.Run(ctx, &mcp.StdioTransport{})
}

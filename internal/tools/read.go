package tools

import (
	"context"
	"net/url"
	"strconv"
	"strings"

	"github.com/bgolat/dn-mcp/internal/dnapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// pageArgs are the pagination controls shared by every list endpoint.
type pageArgs struct {
	PageSize int    `json:"pageSize,omitempty" jsonschema:"Results per page, 1-500. Defaults to 25."`
	Cursor   string `json:"cursor,omitempty" jsonschema:"Cursor from a previous response's pagination.nextCursor, to fetch the next page."`
}

func (p pageArgs) values() url.Values {
	v := url.Values{}
	if p.PageSize > 0 {
		v.Set("pageSize", strconv.Itoa(p.PageSize))
	}
	if p.Cursor != "" {
		v.Set("cursor", p.Cursor)
	}
	return v
}

func encode(v url.Values) string {
	if len(v) == 0 {
		return ""
	}
	return "?" + v.Encode()
}

type listHostsArgs struct {
	pageArgs
	Search          string   `json:"search,omitempty" jsonschema:"Free-text search across host name and IP address."`
	NetworkID       string   `json:"networkID,omitempty" jsonschema:"Only hosts in this network."`
	RoleID          string   `json:"roleID,omitempty" jsonschema:"Only hosts with this role."`
	Tags            []string `json:"tags,omitempty" jsonschema:"Only hosts carrying all of these tags. Each tag is 'key:value', e.g. 'env:prod'."`
	IsBlocked       *bool    `json:"isBlocked,omitempty" jsonschema:"Filter to blocked (true) or unblocked (false) hosts."`
	IsLighthouse    *bool    `json:"isLighthouse,omitempty" jsonschema:"Filter to lighthouses (true) or non-lighthouses (false)."`
	IsRelay         *bool    `json:"isRelay,omitempty" jsonschema:"Filter to relays (true) or non-relays (false)."`
	Platform        string   `json:"platform,omitempty" jsonschema:"Only hosts running this client platform, e.g. linux, darwin, windows, ios, android."`
	NeverSeen       *bool    `json:"neverSeen,omitempty" jsonschema:"True returns only hosts that have never connected; false only hosts that have connected at least once."`
	UpdateAvailable *bool    `json:"updateAvailable,omitempty" jsonschema:"True returns only hosts running an outdated dnclient version."`
}

func (a listHostsArgs) query() string {
	v := a.pageArgs.values()
	set := func(key, val string) {
		if val != "" {
			v.Set("filter."+key, val)
		}
	}
	setBool := func(key string, b *bool) {
		if b != nil {
			v.Set("filter."+key, strconv.FormatBool(*b))
		}
	}
	set("search", a.Search)
	set("networkID", a.NetworkID)
	set("roleID", a.RoleID)
	set("metadata.platform", a.Platform)
	setBool("isBlocked", a.IsBlocked)
	setBool("isLighthouse", a.IsLighthouse)
	setBool("isRelay", a.IsRelay)
	setBool("metadata.updateAvailable", a.UpdateAvailable)
	// metadata.lastSeenAt is an IS NULL filter: null means never seen.
	setBool("metadata.lastSeenAt", a.NeverSeen)
	for _, t := range a.Tags {
		if t = strings.TrimSpace(t); t != "" {
			v.Add("filter.tag", t)
		}
	}
	return encode(v)
}

type hostIDArgs struct {
	HostID string `json:"hostID" jsonschema:"ID of the host."`
}

type networkIDArgs struct {
	NetworkID string `json:"networkID" jsonschema:"ID of the network."`
}

type roleIDArgs struct {
	RoleID string `json:"roleID" jsonschema:"ID of the role."`
}

type routeIDArgs struct {
	RouteID string `json:"routeID" jsonschema:"ID of the route."`
}

func registerReadTools(s *mcp.Server, c *dnapi.Client) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "list_hosts",
		Description: "List hosts in the organization, with optional filters. " +
			"Results are paginated; check pagination.hasNextPage and pass pagination.nextCursor " +
			"as 'cursor' to continue. Use this before any host operation to resolve a host name to its ID.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args listHostsArgs) (*mcp.CallToolResult, any, error) {
		return list(ctx, c, "/v2/hosts"+args.query())
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_host",
		Description: "Fetch one host by ID, including its tags, role, static addresses, listen port, config overrides, and last-seen metadata.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args hostIDArgs) (*mcp.CallToolResult, any, error) {
		return get(ctx, c, "/v2/hosts/"+url.PathEscape(args.HostID))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_networks",
		Description: "List the networks in the organization. Most other operations need a networkID from here.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args pageArgs) (*mcp.CallToolResult, any, error) {
		return list(ctx, c, "/v2/networks"+encode(args.values()))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_network",
		Description: "Fetch one network by ID, including its CIDR ranges and certificate settings.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args networkIDArgs) (*mcp.CallToolResult, any, error) {
		return get(ctx, c, "/v2/networks/"+url.PathEscape(args.NetworkID))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "list_roles",
		Description: "List roles. A role holds the firewall rules that govern what a host may reach. " +
			"This server is read-only for roles: it can show you what a role permits but cannot change firewall rules.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args pageArgs) (*mcp.CallToolResult, any, error) {
		return list(ctx, c, "/v1/roles"+encode(args.values()))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_role",
		Description: "Fetch one role by ID, including its full firewall rule set. Useful for explaining why a host can or cannot reach something.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args roleIDArgs) (*mcp.CallToolResult, any, error) {
		return get(ctx, c, "/v1/roles/"+url.PathEscape(args.RoleID))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_routes",
		Description: "List unsafe routes, which forward traffic from the Nebula network to CIDRs reachable behind a host.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args pageArgs) (*mcp.CallToolResult, any, error) {
		return list(ctx, c, "/v1/routes"+encode(args.values()))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_route",
		Description: "Fetch one unsafe route by ID.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args routeIDArgs) (*mcp.CallToolResult, any, error) {
		return get(ctx, c, "/v1/routes/"+url.PathEscape(args.RouteID))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_tags",
		Description: "List the tags defined in the organization. Tags are 'key:value' strings used to group hosts and target firewall rules.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args pageArgs) (*mcp.CallToolResult, any, error) {
		return list(ctx, c, "/v2/tags"+encode(args.values()))
	})
}

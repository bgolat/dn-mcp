package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/bgolat/dn-mcp/internal/dnapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// configOverride mirrors one entry of a host's configOverrides. The response
// shape ([]ConfigOverride) and the request shape (ConfigOverrideInputs) both
// serialize as {"key":..., "value":...}, so this round-trips unchanged.
type configOverride struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

// hostUpdateBody is the full body accepted by PUT /v3/hosts/{hostID}.
//
// The API treats PUT as a full replacement, not a patch: every field present
// here overwrites stored state, and an omitted field is written as its zero
// value. That makes a naive partial update destructive — sending only
// listenPort would silently clear the host's tags, role and static addresses.
// So update_host always reads the host first and merges onto the current
// state; see updateHost.
type hostUpdateBody struct {
	Name            string           `json:"name"`
	RoleID          *string          `json:"roleID"`
	StaticAddresses []string         `json:"staticAddresses"`
	ListenPort      int              `json:"listenPort"`
	Tags            []string         `json:"tags"`
	ConfigOverrides []configOverride `json:"configOverrides"`
}

type createHostArgs struct {
	NetworkID       string   `json:"networkID" jsonschema:"ID of the network to create the host in. Get it from list_networks."`
	Name            string   `json:"name" jsonschema:"Name for the host. Must be unique within the network."`
	RoleID          string   `json:"roleID,omitempty" jsonschema:"ID of the role to assign, which determines the host's firewall rules. Get it from list_roles."`
	Tags            []string `json:"tags,omitempty" jsonschema:"Tags to assign, each a 'key:value' string such as 'env:prod'."`
	IPAddresses     []string `json:"ipAddresses,omitempty" jsonschema:"Specific IP addresses to assign. Omit to let the API assign one automatically."`
	StaticAddresses []string `json:"staticAddresses,omitempty" jsonschema:"Publicly reachable 'host:port' or 'ip:port' addresses. Required for lighthouses and relays."`
	ListenPort      int      `json:"listenPort,omitempty" jsonschema:"UDP port the host listens on. Lighthouses and relays need a fixed port; leave 0 for roaming clients."`
	IsLighthouse    bool     `json:"isLighthouse,omitempty" jsonschema:"Create this host as a lighthouse."`
	IsRelay         bool     `json:"isRelay,omitempty" jsonschema:"Create this host as a relay."`
}

func (a createHostArgs) body() map[string]any {
	b := map[string]any{
		"networkID":       a.NetworkID,
		"name":            a.Name,
		"staticAddresses": orEmpty(a.StaticAddresses),
		"listenPort":      a.ListenPort,
		"isLighthouse":    a.IsLighthouse,
		"isRelay":         a.IsRelay,
		"tags":            orEmpty(a.Tags),
	}
	if a.RoleID != "" {
		b["roleID"] = a.RoleID
	}
	if len(a.IPAddresses) > 0 {
		b["ipAddresses"] = a.IPAddresses
	}
	return b
}

type createHostAndCodeArgs struct {
	createHostArgs
	CodeLifetimeSeconds int `json:"codeLifetimeSeconds,omitempty" jsonschema:"How long the enrollment code stays valid, in seconds. Omit for the API default."`
}

type updateHostArgs struct {
	HostID string `json:"hostID" jsonschema:"ID of the host to update."`

	// Every mutable field is a pointer so an omitted field means "leave it
	// alone" rather than "set it to zero".
	Name            *string   `json:"name,omitempty" jsonschema:"New name for the host. Omit to leave unchanged."`
	RoleID          *string   `json:"roleID,omitempty" jsonschema:"New role ID, which changes the host's firewall rules. Pass an empty string to unassign the role. Omit to leave unchanged."`
	ListenPort      *int      `json:"listenPort,omitempty" jsonschema:"New UDP listen port. Omit to leave unchanged."`
	StaticAddresses *[]string `json:"staticAddresses,omitempty" jsonschema:"Replacement list of 'host:port' static addresses. This replaces the whole list. Omit to leave unchanged."`
	Tags            *[]string `json:"tags,omitempty" jsonschema:"Replacement list of 'key:value' tags. This replaces the whole list, so include tags you want to keep. Omit to leave tags unchanged."`
}

type enrollmentCodeArgs struct {
	HostID string `json:"hostID" jsonschema:"ID of the host to issue an enrollment code for."`
}

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func registerHostTools(s *mcp.Server, c *dnapi.Client) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "create_host",
		Description: "Create a new host in a network. Returns the created host including its assigned IP address. " +
			"To create a host that someone can immediately enroll, prefer create_host_and_enrollment_code.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args createHostArgs) (*mcp.CallToolResult, any, error) {
		raw, err := c.Do(ctx, "POST", "/v2/hosts", args.body())
		if err != nil {
			return nil, nil, err
		}
		return rawResult(raw)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "create_host_and_enrollment_code",
		Description: "Create a host and issue an enrollment code in one step. The code is what dnclient uses to enroll " +
			"the machine, so this is the usual way to onboard a new device. The code is short-lived and is shown only in this response.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args createHostAndCodeArgs) (*mcp.CallToolResult, any, error) {
		body := args.createHostArgs.body()
		if args.CodeLifetimeSeconds > 0 {
			body["codeLifetimeSeconds"] = args.CodeLifetimeSeconds
		}
		raw, err := c.Do(ctx, "POST", "/v2/host-and-enrollment-code", body)
		if err != nil {
			return nil, nil, err
		}
		return rawResult(raw)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "update_host",
		Description: "Update a host's name, role, listen port, static addresses, or tags. " +
			"Only the fields you pass are changed; everything else is preserved. " +
			"Note that tags and staticAddresses are whole-list replacements, so to add one tag you must pass the existing tags plus the new one — " +
			"call get_host first to read the current list. Config overrides are always preserved and cannot be edited here.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args updateHostArgs) (*mcp.CallToolResult, any, error) {
		return updateHost(ctx, c, args)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "block_host",
		Description: "Block a host, immediately revoking its access to the network. The host keeps its configuration and can be restored with unblock_host. " +
			"This disconnects the machine, so confirm the target with the user first.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args hostIDArgs) (*mcp.CallToolResult, any, error) {
		raw, err := c.Do(ctx, "POST", "/v2/hosts/"+url.PathEscape(args.HostID)+"/block", nil)
		if err != nil {
			return nil, nil, err
		}
		return rawResult(raw)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "unblock_host",
		Description: "Restore network access to a host that was previously blocked.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args hostIDArgs) (*mcp.CallToolResult, any, error) {
		raw, err := c.Do(ctx, "POST", "/v2/hosts/"+url.PathEscape(args.HostID)+"/unblock", nil)
		if err != nil {
			return nil, nil, err
		}
		return rawResult(raw)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "delete_host",
		Description: "Permanently delete a host. This is irreversible: the host's certificate is revoked, its IP address is released, " +
			"and the machine must be re-enrolled to rejoin. Prefer block_host for anything temporary. Always confirm the exact host with the user first.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args hostIDArgs) (*mcp.CallToolResult, any, error) {
		raw, err := c.Do(ctx, "DELETE", "/v1/hosts/"+url.PathEscape(args.HostID), nil)
		if err != nil {
			return nil, nil, err
		}
		if len(raw) == 0 || string(raw) == "null" {
			return jsonResult(map[string]any{"deleted": true, "hostID": args.HostID})
		}
		return rawResult(raw)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "create_enrollment_code",
		Description: "Issue a fresh enrollment code for an existing host, for example to re-enroll a machine that was reinstalled. " +
			"The code is short-lived and is shown only in this response.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args enrollmentCodeArgs) (*mcp.CallToolResult, any, error) {
		raw, err := c.Do(ctx, "POST", "/v1/hosts/"+url.PathEscape(args.HostID)+"/enrollment-code", nil)
		if err != nil {
			return nil, nil, err
		}
		return rawResult(raw)
	})
}

// updateHost performs a read-modify-write against PUT /v3/hosts/{hostID}.
//
// The endpoint replaces the host wholesale, so we fetch current state, overlay
// only the fields the caller actually set, and send the complete body back.
// Without this, an agent asking to "change the listen port" would also wipe the
// host's tags, role and static addresses.
func updateHost(ctx context.Context, c *dnapi.Client, args updateHostArgs) (*mcp.CallToolResult, any, error) {
	hostID := url.PathEscape(args.HostID)

	current, err := c.Do(ctx, "GET", "/v2/hosts/"+hostID, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read host before updating it: %w", err)
	}

	var body hostUpdateBody
	if err := json.Unmarshal(current, &body); err != nil {
		return nil, nil, fmt.Errorf("failed to decode current host state: %w", err)
	}

	if args.Name != nil {
		body.Name = *args.Name
	}
	if args.RoleID != nil {
		if *args.RoleID == "" {
			body.RoleID = nil
		} else {
			body.RoleID = args.RoleID
		}
	}
	if args.ListenPort != nil {
		body.ListenPort = *args.ListenPort
	}
	if args.StaticAddresses != nil {
		body.StaticAddresses = *args.StaticAddresses
	}
	if args.Tags != nil {
		body.Tags = *args.Tags
	}

	// The API rejects a null list where it expects one; normalize before sending.
	body.StaticAddresses = orEmpty(body.StaticAddresses)
	body.Tags = orEmpty(body.Tags)
	body.ConfigOverrides = orEmpty(body.ConfigOverrides)

	updated, err := c.Do(ctx, "PUT", "/v3/hosts/"+hostID, body)
	if err != nil {
		return nil, nil, err
	}
	return rawResult(updated)
}

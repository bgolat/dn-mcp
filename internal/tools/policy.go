package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/bgolat/dn-mcp/internal/dnapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// firewallRule mirrors the API's FirewallRule. The request and response shapes
// are identical, so current rules read from the API round-trip unchanged.
type firewallRule struct {
	Protocol      string     `json:"protocol" jsonschema:"One of ANY, TCP, UDP, ICMP."`
	Description   string     `json:"description,omitempty" jsonschema:"Human-readable note on what the rule is for."`
	AllowedRoleID *string    `json:"allowedRoleID,omitempty" jsonschema:"Only allow traffic from hosts with this role. Omit to allow any role."`
	AllowedTags   []string   `json:"allowedTags,omitempty" jsonschema:"Only allow traffic from hosts carrying all of these 'key:value' tags. Combined with allowedRoleID using AND. Omit to allow any tags."`
	PortRange     *portRange `json:"portRange,omitempty" jsonschema:"Port range to allow. Omit to allow all ports; omit for ICMP."`
}

type portRange struct {
	From int `json:"from" jsonschema:"First port in the range, 1-65535."`
	To   int `json:"to" jsonschema:"Last port in the range, 1-65535. Equal to from for a single port."`
}

// roleUpdateBody is the full body accepted by PUT /v1/roles/{roleID}. Like
// hosts, the endpoint is a full replacement: an omitted firewallRules clears
// every rule on the role. FirewallRules stays raw so rules the caller did not
// touch are sent back byte-for-byte as the API returned them.
type roleUpdateBody struct {
	Description   string          `json:"description"`
	FirewallRules json.RawMessage `json:"firewallRules"`
}

// tagUpdateBody is the full body accepted by PUT /v1/tags/{tag}. Every field
// except firewallRules is a full replacement — an omitted configOverrides is
// erased — while an omitted firewallRules leaves the tag's rules untouched.
type tagUpdateBody struct {
	Description        string          `json:"description"`
	ConfigOverrides    json.RawMessage `json:"configOverrides"`
	RouteSubscriptions []string        `json:"routeSubscriptions"`
	FirewallRules      json.RawMessage `json:"firewallRules,omitempty"`
}

type createRoleArgs struct {
	Name          string         `json:"name" jsonschema:"Name for the role, 1-50 characters. Must be unique. Cannot be changed later."`
	Description   string         `json:"description,omitempty" jsonschema:"Optional description."`
	FirewallRules []firewallRule `json:"firewallRules,omitempty" jsonschema:"Inbound firewall rules for every host assigned this role. Omit for a role that denies all inbound traffic."`
}

type updateRoleArgs struct {
	RoleID        string          `json:"roleID" jsonschema:"ID of the role to update."`
	Description   *string         `json:"description,omitempty" jsonschema:"New description. Omit to leave unchanged."`
	FirewallRules *[]firewallRule `json:"firewallRules,omitempty" jsonschema:"Replacement list of inbound firewall rules. This replaces the whole list, so include rules you want to keep — call get_role first. Pass an empty list to remove all rules. Omit to leave rules unchanged."`
}

type tagArgs struct {
	Tag string `json:"tag" jsonschema:"The tag, as 'key:value'."`
}

type createTagArgs struct {
	Name               string         `json:"name" jsonschema:"The tag as 'key:value'. Key at most 20 characters, value at most 50, no surrounding whitespace."`
	Description        string         `json:"description,omitempty" jsonschema:"Optional description."`
	FirewallRules      []firewallRule `json:"firewallRules,omitempty" jsonschema:"Inbound firewall rules applied to every host carrying this tag, in addition to its role's rules."`
	RouteSubscriptions []string       `json:"routeSubscriptions,omitempty" jsonschema:"Route IDs that hosts carrying this tag subscribe to."`
}

type updateTagArgs struct {
	Tag                string          `json:"tag" jsonschema:"The tag to update, as 'key:value'."`
	Description        *string         `json:"description,omitempty" jsonschema:"New description. Omit to leave unchanged."`
	FirewallRules      *[]firewallRule `json:"firewallRules,omitempty" jsonschema:"Replacement list of inbound firewall rules. This replaces the whole list, so include rules you want to keep — call get_tag first. Pass an empty list to remove all rules. Omit to leave rules unchanged."`
	RouteSubscriptions *[]string       `json:"routeSubscriptions,omitempty" jsonschema:"Replacement list of route IDs. Omit to leave unchanged."`
}

func orEmptyRaw(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage("[]")
	}
	return raw
}

func registerPolicyTools(s *mcp.Server, c *dnapi.Client) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "create_role",
		Description: "Create a role with inbound firewall rules. A host has exactly one role, and its rules apply to every host assigned it. " +
			"Firewall rules grant network access, so confirm the rules with the user before creating the role.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args createRoleArgs) (*mcp.CallToolResult, any, error) {
		body := map[string]any{
			"name":          args.Name,
			"description":   args.Description,
			"firewallRules": orEmpty(args.FirewallRules),
		}
		raw, err := c.Do(ctx, "POST", "/v1/roles", body)
		if err != nil {
			return nil, nil, err
		}
		return rawResult(raw)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "update_role",
		Description: "Update a role's description or firewall rules. Only the fields you pass are changed. " +
			"firewallRules is a whole-list replacement, so to add one rule pass the existing rules plus the new one — call get_role first. " +
			"This changes what can reach every host with the role, so confirm the change with the user first. Roles cannot be renamed.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args updateRoleArgs) (*mcp.CallToolResult, any, error) {
		return updateRole(ctx, c, args)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "delete_role",
		Description: "Permanently delete a role and its firewall rules. This is irreversible. " +
			"Check its hostCount with get_role first and confirm the exact role with the user.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args roleIDArgs) (*mcp.CallToolResult, any, error) {
		return deleteResult(ctx, c, "/v1/roles/"+url.PathEscape(args.RoleID), map[string]any{"deleted": true, "roleID": args.RoleID})
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "get_tag",
		Description: "Fetch one tag, including its inbound firewall rules, config overrides and route subscriptions. " +
			"list_tags does not return firewall rules, so use this to see what a tag actually allows.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args tagArgs) (*mcp.CallToolResult, any, error) {
		return get(ctx, c, "/v1/tags/"+url.PathEscape(args.Tag))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "create_tag",
		Description: "Create a tag, optionally with inbound firewall rules. A host's firewall is the union of its role's rules and the rules of every tag it carries. " +
			"Firewall rules grant network access, so confirm the rules with the user before creating the tag.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args createTagArgs) (*mcp.CallToolResult, any, error) {
		body := map[string]any{
			"name":               args.Name,
			"description":        args.Description,
			"firewallRules":      orEmpty(args.FirewallRules),
			"routeSubscriptions": orEmpty(args.RouteSubscriptions),
		}
		raw, err := c.Do(ctx, "POST", "/v1/tags", body)
		if err != nil {
			return nil, nil, err
		}
		return rawResult(raw)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "update_tag",
		Description: "Update a tag's description, firewall rules or route subscriptions. Only the fields you pass are changed; config overrides are always preserved. " +
			"firewallRules is a whole-list replacement, so to add one rule pass the existing rules plus the new one — call get_tag first. " +
			"This changes what can reach every host carrying the tag, so confirm the change with the user first.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args updateTagArgs) (*mcp.CallToolResult, any, error) {
		return updateTag(ctx, c, args)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "delete_tag",
		Description: "Permanently delete a tag, removing it and its firewall rules from every host that carries it. This is irreversible. " +
			"Check its hostCount with get_tag first and confirm the exact tag with the user.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args tagArgs) (*mcp.CallToolResult, any, error) {
		return deleteResult(ctx, c, "/v1/tags/"+url.PathEscape(args.Tag), map[string]any{"deleted": true, "tag": args.Tag})
	})
}

func deleteResult(ctx context.Context, c *dnapi.Client, path string, fallback map[string]any) (*mcp.CallToolResult, any, error) {
	raw, err := c.Do(ctx, "DELETE", path, nil)
	if err != nil {
		return nil, nil, err
	}
	if len(raw) == 0 || string(raw) == "null" || string(raw) == "{}" {
		return jsonResult(fallback)
	}
	return rawResult(raw)
}

// updateRole performs a read-modify-write against PUT /v1/roles/{roleID}, for
// the same reason as updateHost: the PUT is a full replacement, and a request
// carrying only a new description would otherwise delete every firewall rule.
func updateRole(ctx context.Context, c *dnapi.Client, args updateRoleArgs) (*mcp.CallToolResult, any, error) {
	roleID := url.PathEscape(args.RoleID)

	current, err := c.Do(ctx, "GET", "/v1/roles/"+roleID, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read role before updating it: %w", err)
	}

	var body roleUpdateBody
	if err := json.Unmarshal(current, &body); err != nil {
		return nil, nil, fmt.Errorf("failed to decode current role state: %w", err)
	}

	if args.Description != nil {
		body.Description = *args.Description
	}
	if args.FirewallRules != nil {
		rules, err := json.Marshal(orEmpty(*args.FirewallRules))
		if err != nil {
			return nil, nil, fmt.Errorf("failed to encode firewall rules: %w", err)
		}
		body.FirewallRules = rules
	} else if len(body.FirewallRules) == 0 {
		// The read carried no rules field at all. Sending the PUT anyway would
		// clear whatever rules the role really has, so refuse instead.
		return nil, nil, fmt.Errorf("role %s was returned without its firewall rules; refusing to update it, since that would erase them", args.RoleID)
	}
	body.FirewallRules = orEmptyRaw(body.FirewallRules)

	updated, err := c.Do(ctx, "PUT", "/v1/roles/"+roleID, body)
	if err != nil {
		return nil, nil, err
	}
	return rawResult(updated)
}

// updateTag performs a read-modify-write against PUT /v1/tags/{tag}. The PUT
// replaces description, configOverrides and routeSubscriptions wholesale, so
// all three are read back and resent; firewallRules is only sent when the
// caller changes it, since omitting it is how the API leaves rules untouched.
func updateTag(ctx context.Context, c *dnapi.Client, args updateTagArgs) (*mcp.CallToolResult, any, error) {
	tag := url.PathEscape(args.Tag)

	current, err := c.Do(ctx, "GET", "/v1/tags/"+tag, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read tag before updating it: %w", err)
	}

	var body tagUpdateBody
	if err := json.Unmarshal(current, &body); err != nil {
		return nil, nil, fmt.Errorf("failed to decode current tag state: %w", err)
	}
	body.FirewallRules = nil

	if args.Description != nil {
		body.Description = *args.Description
	}
	if args.RouteSubscriptions != nil {
		body.RouteSubscriptions = *args.RouteSubscriptions
	}
	if args.FirewallRules != nil {
		rules, err := json.Marshal(orEmpty(*args.FirewallRules))
		if err != nil {
			return nil, nil, fmt.Errorf("failed to encode firewall rules: %w", err)
		}
		body.FirewallRules = rules
	}

	body.ConfigOverrides = orEmptyRaw(body.ConfigOverrides)
	body.RouteSubscriptions = orEmpty(body.RouteSubscriptions)

	updated, err := c.Do(ctx, "PUT", "/v1/tags/"+tag, body)
	if err != nil {
		return nil, nil, err
	}
	return rawResult(updated)
}

package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bgolat/dn-mcp/internal/dnapi"
)

// currentRole is what GET /v1/roles/{id} returns in these tests.
const currentRole = `{
  "id": "role-1",
  "name": "Server",
  "description": "web tier",
  "firewallRules": [
    {"protocol": "ICMP", "description": "ping", "allowedRoleID": null, "allowedTags": null, "portRange": null},
    {"protocol": "TCP", "description": "ssh", "allowedRoleID": "role-2", "portRange": {"from": 22, "to": 22}}
  ]
}`

// currentTag is what GET /v1/tags/{tag} returns in these tests. It carries a
// value in every field that PUT /v1/tags/{tag} replaces.
const currentTag = `{
  "name": "tier:edge",
  "description": "edge hosts",
  "priority": 5,
  "hostCount": 6,
  "configOverrides": [{"key": "logging.level", "value": "debug"}],
  "routeSubscriptions": ["route-1"],
  "firewallRules": [{"protocol": "TCP", "portRange": {"from": 443, "to": 443}}]
}`

// newFakePolicyAPI serves getBody for GET path and records the PUT body. The
// raw PUT body is kept alongside the decoded map so tests can tell an omitted
// field from one sent as null.
func newFakePolicyAPI(t *testing.T, path, getBody string, gotPut *map[string]any) *dnapi.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == path:
			io.WriteString(w, `{"data":`+getBody+`}`)
		case r.Method == http.MethodPut && r.URL.Path == path:
			body, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(body, gotPut); err != nil {
				t.Errorf("failed to decode PUT body: %v", err)
			}
			io.WriteString(w, `{"data":`+getBody+`}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return dnapi.New(srv.URL, "test-key")
}

// The role PUT is a full replacement: changing only the description must not
// delete the role's firewall rules.
func TestUpdateRolePreservesRules(t *testing.T) {
	var put map[string]any
	c := newFakePolicyAPI(t, "/v1/roles/role-1", currentRole, &put)

	desc := "renamed tier"
	if _, _, err := updateRole(context.Background(), c, updateRoleArgs{RoleID: "role-1", Description: &desc}); err != nil {
		t.Fatalf("updateRole failed: %v", err)
	}

	if got := put["description"]; got != "renamed tier" {
		t.Errorf("description = %v, want renamed tier", got)
	}
	rules, _ := put["firewallRules"].([]any)
	if len(rules) != 2 {
		t.Fatalf("firewallRules = %v, want both existing rules preserved", put["firewallRules"])
	}
	ssh := rules[1].(map[string]any)
	if ssh["allowedRoleID"] != "role-2" || ssh["portRange"].(map[string]any)["from"] != float64(22) {
		t.Errorf("ssh rule = %v, want it preserved unchanged", ssh)
	}
}

func TestUpdateRoleReplacesRules(t *testing.T) {
	var put map[string]any
	c := newFakePolicyAPI(t, "/v1/roles/role-1", currentRole, &put)

	tags := []string{"team:netops"}
	rules := []firewallRule{{Protocol: "TCP", AllowedTags: tags, PortRange: &portRange{From: 22, To: 22}}}
	if _, _, err := updateRole(context.Background(), c, updateRoleArgs{RoleID: "role-1", FirewallRules: &rules}); err != nil {
		t.Fatalf("updateRole failed: %v", err)
	}

	got, _ := put["firewallRules"].([]any)
	if len(got) != 1 {
		t.Fatalf("firewallRules = %v, want the single replacement rule", put["firewallRules"])
	}
	rule := got[0].(map[string]any)
	if _, ok := rule["allowedRoleID"]; ok {
		t.Errorf("allowedRoleID = %v, want it omitted so any role is allowed", rule["allowedRoleID"])
	}
	if got := put["description"]; got != "web tier" {
		t.Errorf("description = %v, want it preserved as web tier", got)
	}
}

// An explicit empty list is how a caller removes every rule, and must reach
// the API as [] rather than being mistaken for "leave unchanged".
func TestUpdateRoleClearsRules(t *testing.T) {
	var put map[string]any
	c := newFakePolicyAPI(t, "/v1/roles/role-1", currentRole, &put)

	empty := []firewallRule{}
	if _, _, err := updateRole(context.Background(), c, updateRoleArgs{RoleID: "role-1", FirewallRules: &empty}); err != nil {
		t.Fatalf("updateRole failed: %v", err)
	}
	got, ok := put["firewallRules"].([]any)
	if !ok || len(got) != 0 {
		t.Errorf("firewallRules = %v, want an explicit empty list", put["firewallRules"])
	}
}

// If the read comes back without a firewallRules field, a PUT would erase the
// role's real rules. The update must refuse instead.
func TestUpdateRoleRefusesWhenRulesMissingFromRead(t *testing.T) {
	putCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			putCalled = true
		}
		io.WriteString(w, `{"data":{"id":"role-1","name":"Server","description":"x"}}`)
	}))
	defer srv.Close()

	desc := "y"
	c := dnapi.New(srv.URL, "test-key")
	if _, _, err := updateRole(context.Background(), c, updateRoleArgs{RoleID: "role-1", Description: &desc}); err == nil {
		t.Fatal("expected an error when the read omits firewall rules")
	}
	if putCalled {
		t.Error("PUT was issued without the role's rules; this would erase them")
	}
}

func TestUpdateRoleAbortsWhenReadFails(t *testing.T) {
	putCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			putCalled = true
		}
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"errors":[{"code":"NOT_FOUND","message":"role does not exist"}]}`)
	}))
	defer srv.Close()

	desc := "y"
	c := dnapi.New(srv.URL, "test-key")
	if _, _, err := updateRole(context.Background(), c, updateRoleArgs{RoleID: "role-1", Description: &desc}); err == nil {
		t.Fatal("expected an error when the role cannot be read")
	}
	if putCalled {
		t.Error("PUT was issued despite the read failing")
	}
}

// The tag PUT replaces config overrides and route subscriptions wholesale, so
// a rules-only change must resend both.
func TestUpdateTagPreservesOverridesAndRoutes(t *testing.T) {
	var put map[string]any
	c := newFakePolicyAPI(t, "/v1/tags/tier:edge", currentTag, &put)

	rules := []firewallRule{{Protocol: "ICMP"}}
	if _, _, err := updateTag(context.Background(), c, updateTagArgs{Tag: "tier:edge", FirewallRules: &rules}); err != nil {
		t.Fatalf("updateTag failed: %v", err)
	}

	overrides, _ := put["configOverrides"].([]any)
	if len(overrides) != 1 || overrides[0].(map[string]any)["key"] != "logging.level" {
		t.Errorf("configOverrides = %v, want the existing override preserved", put["configOverrides"])
	}
	routes, _ := put["routeSubscriptions"].([]any)
	if len(routes) != 1 || routes[0] != "route-1" {
		t.Errorf("routeSubscriptions = %v, want route-1 preserved", put["routeSubscriptions"])
	}
	if got := put["description"]; got != "edge hosts" {
		t.Errorf("description = %v, want it preserved", got)
	}
	got, _ := put["firewallRules"].([]any)
	if len(got) != 1 || got[0].(map[string]any)["protocol"] != "ICMP" {
		t.Errorf("firewallRules = %v, want the replacement rule", put["firewallRules"])
	}
}

// Omitting firewallRules is the API's way to leave a tag's rules alone, so a
// description-only change must not send the field at all.
func TestUpdateTagLeavesRulesUntouchedWhenOmitted(t *testing.T) {
	var put map[string]any
	c := newFakePolicyAPI(t, "/v1/tags/tier:edge", currentTag, &put)

	desc := "edge"
	if _, _, err := updateTag(context.Background(), c, updateTagArgs{Tag: "tier:edge", Description: &desc}); err != nil {
		t.Fatalf("updateTag failed: %v", err)
	}
	if v, ok := put["firewallRules"]; ok {
		t.Errorf("firewallRules = %v, want the field omitted so existing rules are untouched", v)
	}
	if put["priority"] != nil || put["before"] != nil || put["after"] != nil {
		t.Errorf("PUT carried ordering fields %v; the tag's priority must not change", put)
	}
}

func TestUpdateTagClearsRules(t *testing.T) {
	var put map[string]any
	c := newFakePolicyAPI(t, "/v1/tags/tier:edge", currentTag, &put)

	empty := []firewallRule{}
	if _, _, err := updateTag(context.Background(), c, updateTagArgs{Tag: "tier:edge", FirewallRules: &empty}); err != nil {
		t.Fatalf("updateTag failed: %v", err)
	}
	got, ok := put["firewallRules"].([]any)
	if !ok || len(got) != 0 {
		t.Errorf("firewallRules = %v, want an explicit empty list", put["firewallRules"])
	}
}

func TestUpdateTagNormalizesNullLists(t *testing.T) {
	var put map[string]any
	bare := `{"name":"tier:edge","description":"","configOverrides":null,"routeSubscriptions":null}`
	c := newFakePolicyAPI(t, "/v1/tags/tier:edge", bare, &put)

	desc := "edge"
	if _, _, err := updateTag(context.Background(), c, updateTagArgs{Tag: "tier:edge", Description: &desc}); err != nil {
		t.Fatalf("updateTag failed: %v", err)
	}
	for _, field := range []string{"configOverrides", "routeSubscriptions"} {
		if got, ok := put[field]; !ok || got == nil {
			t.Errorf("%s = %v, want an empty list rather than null or missing", field, got)
		}
	}
}

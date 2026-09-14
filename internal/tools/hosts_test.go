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

// currentHost is what GET /v2/hosts/{id} returns in these tests. It carries a
// value in every field that PUT /v3/hosts/{id} replaces.
const currentHost = `{
  "id": "host-1",
  "networkID": "net-1",
  "roleID": "role-7",
  "name": "web-01",
  "ipAddresses": ["100.100.0.5"],
  "staticAddresses": ["1.2.3.4:4242"],
  "listenPort": 4242,
  "isLighthouse": false,
  "isRelay": false,
  "tags": ["env:prod", "team:infra"],
  "configOverrides": [{"key": "lighthouse.interval", "value": 60}]
}`

// newFakeAPI serves the host above and records the body of the PUT.
func newFakeAPI(t *testing.T, gotPut *map[string]any) *dnapi.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v2/hosts/host-1":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"data":`+currentHost+`}`)

		case r.Method == http.MethodPut && r.URL.Path == "/v3/hosts/host-1":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("failed to read PUT body: %v", err)
			}
			if err := json.Unmarshal(body, gotPut); err != nil {
				t.Errorf("failed to decode PUT body: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"data":`+currentHost+`}`)

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return dnapi.New(srv.URL, "test-key")
}

// TestUpdateHostPreservesUnsetFields is the central safety property of this
// server: PUT is a full replacement, so a partial update must not blank out
// fields the caller never mentioned.
func TestUpdateHostPreservesUnsetFields(t *testing.T) {
	var put map[string]any
	c := newFakeAPI(t, &put)

	port := 4243
	if _, _, err := updateHost(context.Background(), c, updateHostArgs{
		HostID:     "host-1",
		ListenPort: &port,
	}); err != nil {
		t.Fatalf("updateHost failed: %v", err)
	}

	if got := put["listenPort"]; got != float64(4243) {
		t.Errorf("listenPort = %v, want 4243", got)
	}
	if got := put["name"]; got != "web-01" {
		t.Errorf("name = %v, want it preserved as web-01", got)
	}
	if got := put["roleID"]; got != "role-7" {
		t.Errorf("roleID = %v, want it preserved as role-7", got)
	}

	tags, _ := put["tags"].([]any)
	if len(tags) != 2 || tags[0] != "env:prod" || tags[1] != "team:infra" {
		t.Errorf("tags = %v, want both preserved", put["tags"])
	}

	addrs, _ := put["staticAddresses"].([]any)
	if len(addrs) != 1 || addrs[0] != "1.2.3.4:4242" {
		t.Errorf("staticAddresses = %v, want it preserved", put["staticAddresses"])
	}

	overrides, _ := put["configOverrides"].([]any)
	if len(overrides) != 1 {
		t.Fatalf("configOverrides = %v, want the existing override preserved", put["configOverrides"])
	}
	if key := overrides[0].(map[string]any)["key"]; key != "lighthouse.interval" {
		t.Errorf("config override key = %v, want lighthouse.interval", key)
	}
}

func TestUpdateHostAppliesSetFields(t *testing.T) {
	var put map[string]any
	c := newFakeAPI(t, &put)

	name := "web-02"
	tags := []string{"env:staging"}
	if _, _, err := updateHost(context.Background(), c, updateHostArgs{
		HostID: "host-1",
		Name:   &name,
		Tags:   &tags,
	}); err != nil {
		t.Fatalf("updateHost failed: %v", err)
	}

	if got := put["name"]; got != "web-02" {
		t.Errorf("name = %v, want web-02", got)
	}
	got, _ := put["tags"].([]any)
	if len(got) != 1 || got[0] != "env:staging" {
		t.Errorf("tags = %v, want the replacement list", put["tags"])
	}
	// Unmentioned fields still ride along untouched.
	if got := put["listenPort"]; got != float64(4242) {
		t.Errorf("listenPort = %v, want it preserved as 4242", got)
	}
}

// An empty roleID is the documented way to unassign a role, and must reach the
// API as null rather than as the empty string.
func TestUpdateHostUnassignsRole(t *testing.T) {
	var put map[string]any
	c := newFakeAPI(t, &put)

	empty := ""
	if _, _, err := updateHost(context.Background(), c, updateHostArgs{
		HostID: "host-1",
		RoleID: &empty,
	}); err != nil {
		t.Fatalf("updateHost failed: %v", err)
	}

	if got, ok := put["roleID"]; !ok || got != nil {
		t.Errorf("roleID = %v, want nil to unassign the role", got)
	}
}

// Lists must never be sent as null: the API expects a list where one is
// declared, and null round-trips badly for staticAddresses in particular.
func TestUpdateHostNormalizesNullLists(t *testing.T) {
	var put map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			io.WriteString(w, `{"data":{"id":"host-1","name":"bare","listenPort":0,
				"staticAddresses":null,"tags":null,"configOverrides":null}}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &put)
		io.WriteString(w, `{"data":{}}`)
	}))
	defer srv.Close()

	name := "renamed"
	c := dnapi.New(srv.URL, "test-key")
	if _, _, err := updateHost(context.Background(), c, updateHostArgs{
		HostID: "host-1",
		Name:   &name,
	}); err != nil {
		t.Fatalf("updateHost failed: %v", err)
	}

	for _, field := range []string{"staticAddresses", "tags", "configOverrides"} {
		got, ok := put[field]
		if !ok {
			t.Errorf("%s missing from PUT body", field)
			continue
		}
		if got == nil {
			t.Errorf("%s was sent as null, want an empty list", field)
		}
	}
}

// A failed read must abort the write rather than PUT a zero-valued host.
func TestUpdateHostAbortsWhenReadFails(t *testing.T) {
	putCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			putCalled = true
		}
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"errors":[{"code":"NOT_FOUND","message":"host does not exist"}]}`)
	}))
	defer srv.Close()

	name := "whatever"
	c := dnapi.New(srv.URL, "test-key")
	_, _, err := updateHost(context.Background(), c, updateHostArgs{HostID: "host-1", Name: &name})
	if err == nil {
		t.Fatal("expected an error when the host cannot be read")
	}
	if putCalled {
		t.Error("PUT was issued despite the read failing; this would blank the host")
	}
}

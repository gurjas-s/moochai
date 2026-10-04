package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"peerai-serv/internal/auth"
	"peerai-serv/internal/registry"
)

func newMux() (*http.ServeMux, *auth.Store, *registry.Registry) {
	reg := registry.New(time.Minute)
	store := auth.New()
	mux := http.NewServeMux()
	New(reg, store, nil).Register(mux)
	return mux, store, reg
}

func doReq(mux *http.ServeMux, method, path, body, key, remote string) *httptest.ResponseRecorder {
	var rdr *strings.Reader
	if body == "" {
		rdr = strings.NewReader("")
	} else {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if remote != "" {
		req.RemoteAddr = remote
	} else {
		req.RemoteAddr = "127.0.0.1:5000"
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func createUser(t *testing.T, mux *http.ServeMux, name string) (string, string) {
	t.Helper()
	rec := doReq(mux, "POST", "/api/users", `{"name":"`+name+`"}`, "", "")
	if rec.Code != 201 {
		t.Fatalf("create user %s: status %d: %s", name, rec.Code, rec.Body)
	}
	var out struct {
		UserID string `json:"user_id"`
		APIKey string `json:"api_key"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.UserID == "" || out.APIKey == "" {
		t.Fatalf("missing id/key: %s", rec.Body)
	}
	return out.UserID, out.APIKey
}

func TestUpsert(t *testing.T) {
	mux, store, reg := newMux()
	_, key := createUser(t, mux, "alice")
	_ = store

	tests := []struct {
		name, remote, body, key string
		want                    int
	}{
		{"ok", "100.64.0.5:5000", `{"node_id":"n1","tailscale_ip":"100.64.0.5","listen_addr":"100.64.0.5:9100"}`, key, 200},
		{"loopback ok", "127.0.0.1:5000", `{"node_id":"n2","tailscale_ip":"100.64.0.6","listen_addr":"100.64.0.6:9100"}`, key, 200},
		{"no key", "100.64.0.5:5000", `{"node_id":"n9","tailscale_ip":"100.64.0.5","listen_addr":"100.64.0.5:9100"}`, "", 401},
		{"bad key", "100.64.0.5:5000", `{"node_id":"n9","tailscale_ip":"100.64.0.5","listen_addr":"100.64.0.5:9100"}`, "wrong", 401},
		{"spoofed ip", "100.64.0.9:5000", `{"node_id":"n3","tailscale_ip":"100.64.0.5","listen_addr":"100.64.0.5:9100"}`, key, 400},
		{"addr mismatch", "100.64.0.5:5000", `{"node_id":"n4","tailscale_ip":"100.64.0.5","listen_addr":"10.0.0.1:9100"}`, key, 400},
		{"no id", "100.64.0.5:5000", `{"tailscale_ip":"100.64.0.5","listen_addr":"100.64.0.5:9100"}`, key, 400},
		{"bad json", "100.64.0.5:5000", `{`, key, 400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := doReq(mux, "POST", "/api/nodes/heartbeat", tt.body, tt.key, tt.remote)
			if rec.Code != tt.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, tt.want, rec.Body)
			}
		})
	}
	if reg.Len() != 2 {
		t.Fatalf("registry has %d nodes, want 2", reg.Len())
	}
}

func TestUsersGroupsNodes(t *testing.T) {
	mux, _, _ := newMux()
	aliceID, aliceKey := createUser(t, mux, "alice")
	bobID, bobKey := createUser(t, mux, "bob")

	// Duplicate name fails.
	if rec := doReq(mux, "POST", "/api/users", `{"name":"alice"}`, "", ""); rec.Code != 400 {
		t.Fatalf("duplicate name: status %d, want 400", rec.Code)
	}
	// Me works with key.
	if rec := doReq(mux, "GET", "/api/me", "", aliceKey, ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), aliceID) {
		t.Fatalf("me = %d %s", rec.Code, rec.Body)
	}
	if rec := doReq(mux, "GET", "/api/me", "", "", ""); rec.Code != 401 {
		t.Fatalf("me without key: status %d, want 401", rec.Code)
	}

	// Alice creates a group and adds Bob.
	rec := doReq(mux, "POST", "/api/groups", `{"name":"lab"}`, aliceKey, "")
	if rec.Code != 201 {
		t.Fatalf("create group: %d %s", rec.Code, rec.Body)
	}
	var g struct {
		GroupID string `json:"group_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &g); err != nil {
		t.Fatal(err)
	}
	// Bob sees no groups yet.
	if rec := doReq(mux, "GET", "/api/groups", "", bobKey, ""); rec.Code != 200 || strings.Contains(rec.Body.String(), g.GroupID) {
		t.Fatalf("bob groups before join = %d %s", rec.Code, rec.Body)
	}
	// Non-owner cannot add.
	if rec := doReq(mux, "POST", "/api/groups/"+g.GroupID+"/members", `{"user_id":"`+aliceID+`"}`, bobKey, ""); rec.Code != 403 {
		t.Fatalf("non-owner add: status %d, want 403", rec.Code)
	}
	rec = doReq(mux, "POST", "/api/groups/"+g.GroupID+"/members", `{"user_id":"`+bobID+`"}`, aliceKey, "")
	if rec.Code != 200 {
		t.Fatalf("owner add: %d %s", rec.Code, rec.Body)
	}
	// Bob now sees the group.
	if rec := doReq(mux, "GET", "/api/groups", "", bobKey, ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), g.GroupID) {
		t.Fatalf("bob groups after join = %d %s", rec.Code, rec.Body)
	}
	// Outsider cannot read group.
	_, carolKey := createUser(t, mux, "carol")
	if rec := doReq(mux, "GET", "/api/groups/"+g.GroupID, "", carolKey, ""); rec.Code != 404 {
		t.Fatalf("outsider read: status %d, want 404", rec.Code)
	}

	// Alice registers a node shared with the group.
	nodeBody := `{"node_id":"n1","tailscale_ip":"100.64.0.5","listen_addr":"100.64.0.5:9100","services":[{"id":"s","models":["qwen"],"healthy":true}],"groups":["` + g.GroupID + `"]}`
	if rec := doReq(mux, "POST", "/api/nodes/register", nodeBody, aliceKey, "100.64.0.5:5000"); rec.Code != 200 {
		t.Fatalf("register shared node: %d %s", rec.Code, rec.Body)
	}
	// Unknown group fails.
	badBody := `{"node_id":"n2","tailscale_ip":"100.64.0.5","listen_addr":"100.64.0.5:9100","groups":["g_missing"]}`
	if rec := doReq(mux, "POST", "/api/nodes/register", badBody, aliceKey, "100.64.0.5:5000"); rec.Code != 400 {
		t.Fatalf("bad group: status %d, want 400", rec.Code)
	}
	// Carol cannot use a group she is not in.
	carolBody := `{"node_id":"n3","tailscale_ip":"100.64.0.5","listen_addr":"100.64.0.5:9100","groups":["` + g.GroupID + `"]}`
	if rec := doReq(mux, "POST", "/api/nodes/register", carolBody, carolKey, "100.64.0.5:5000"); rec.Code != 400 {
		t.Fatalf("non-member share: status %d, want 400", rec.Code)
	}

	// Bob sees the node, Carol does not.
	if rec := doReq(mux, "GET", "/api/nodes", "", bobKey, ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "n1") {
		t.Fatalf("bob nodes = %d %s", rec.Code, rec.Body)
	}
	if rec := doReq(mux, "GET", "/api/nodes", "", carolKey, ""); rec.Code != 200 || strings.Contains(rec.Body.String(), "n1") {
		t.Fatalf("carol nodes = %d %s", rec.Code, rec.Body)
	}

	// Bob leaves, then loses access.
	if rec := doReq(mux, "DELETE", "/api/groups/"+g.GroupID+"/members/"+bobID, "", bobKey, ""); rec.Code != 200 {
		t.Fatalf("leave: %d %s", rec.Code, rec.Body)
	}
	if rec := doReq(mux, "GET", "/api/nodes", "", bobKey, ""); rec.Code != 200 || strings.Contains(rec.Body.String(), "n1") {
		t.Fatalf("bob nodes after leave = %d %s", rec.Code, rec.Body)
	}
	// Owner cannot leave.
	if rec := doReq(mux, "DELETE", "/api/groups/"+g.GroupID+"/members/"+aliceID, "", aliceKey, ""); rec.Code != 400 {
		t.Fatalf("owner leave: status %d, want 400", rec.Code)
	}
}

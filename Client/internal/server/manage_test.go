package server

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"peer-ai-client/internal/manage"
)

func splitCentral(t *testing.T, url string) (string, string) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(strings.TrimPrefix(url, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := strconv.Atoi(portStr); err != nil {
		t.Fatal(err)
	}
	return host, portStr
}

func manageOptions() Options {
	opts := testOptions()
	opts.Manage = manage.New("")
	opts.Setup = manage.NewSetup("")
	return opts
}

func serveManage(t *testing.T, s *Server, method, path, body, remote string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *strings.Reader
	if body == "" {
		rdr = strings.NewReader("")
	} else {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestSetupStatusHidesKey(t *testing.T) {
	s := New(manageOptions())
	rec := serveManage(t, s, http.MethodGet, "/manage/setup", "", "127.0.0.1:5000")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "api_key_set") {
		t.Fatalf("setup status = %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "peerai_") {
		t.Fatal("setup status must not return the key")
	}
}

func TestSetupSaveAndLoopbackGuard(t *testing.T) {
	dir := t.TempDir()
	opts := testOptions()
	opts.Manage = manage.New("")
	opts.Setup = manage.NewSetup(dir + "/credentials.yaml")
	s := New(opts)
	body := `{"central_host":"100.64.0.10","central_port":8080,"api_key":"peerai_test"}`
	if rec := serveManage(t, s, http.MethodPut, "/manage/setup", body, "127.0.0.1:5000"); rec.Code != http.StatusOK {
		t.Fatalf("setup save = %d %s, want 200", rec.Code, rec.Body.String())
	}
	stored, err := opts.Setup.Load()
	if err != nil || stored.APIKey != "peerai_test" || stored.CentralPort != 8080 {
		t.Fatalf("stored = %+v, %v", stored, err)
	}
	if rec := serveManage(t, s, http.MethodPut, "/manage/setup", body, "100.64.0.5:5000"); rec.Code != http.StatusForbidden {
		t.Fatalf("non-loopback setup save = %d, want 403", rec.Code)
	}
	if rec := serveManage(t, s, http.MethodPut, "/manage/setup", `{"central_host":"h"}`, "127.0.0.1:5000"); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid setup save = %d, want 400", rec.Code)
	}
}

func TestRegisterCreatesUserOnCentral(t *testing.T) {
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/users" {
			t.Errorf("central path = %q, want /api/users", r.URL.Path)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"user_id":"u_1","api_key":"peerai_new","name":"bob"}`))
	}))
	defer central.Close()
	host, port := splitCentral(t, central.URL)
	dir := t.TempDir()
	opts := testOptions()
	opts.Manage = manage.New("")
	opts.Setup = manage.NewSetup(dir + "/credentials.yaml")
	s := New(opts)
	body := `{"central_host":"` + host + `","central_port":` + port + `,"name":"bob"}`
	rec := serveManage(t, s, http.MethodPost, "/manage/register", body, "127.0.0.1:5000")
	if rec.Code != http.StatusCreated {
		t.Fatalf("register = %d %s, want 201", rec.Code, rec.Body.String())
	}
	stored, err := opts.Setup.Load()
	if err != nil || stored.APIKey != "peerai_new" {
		t.Fatalf("stored = %+v, %v", stored, err)
	}
}

func TestRegisterFailsWhenCentralDown(t *testing.T) {
	s := New(manageOptions())
	body := `{"central_host":"127.0.0.1","central_port":1,"name":"bob"}`
	rec := serveManage(t, s, http.MethodPost, "/manage/register", body, "127.0.0.1:5000")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("register with dead central = %d, want 502", rec.Code)
	}
}

func TestManageBackendsLoopback(t *testing.T) {
	s := New(manageOptions())
	add := `{"name":"ollama","endpoint":"http://127.0.0.1:11434","provider":"ollama"}`
	if rec := serveManage(t, s, http.MethodPost, "/manage/backends", add, "127.0.0.1:5000"); rec.Code != http.StatusCreated {
		t.Fatalf("add = %d %s, want 201", rec.Code, rec.Body.String())
	}
	if rec := serveManage(t, s, http.MethodGet, "/manage/backends", "", "127.0.0.1:5000"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ollama") {
		t.Fatalf("list = %d %s", rec.Code, rec.Body.String())
	}
	if rec := serveManage(t, s, http.MethodGet, "/manage/backends", "", "100.64.0.5:5000"); rec.Code != http.StatusForbidden {
		t.Fatalf("non-loopback list = %d, want 403", rec.Code)
	}
	if rec := serveManage(t, s, http.MethodDelete, "/manage/backends/ollama", "", "127.0.0.1:5000"); rec.Code != http.StatusOK {
		t.Fatalf("delete = %d %s, want 200", rec.Code, rec.Body.String())
	}
	if rec := serveManage(t, s, http.MethodPost, "/manage/backends", `{"name":""}`, "127.0.0.1:5000"); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid add = %d, want 400", rec.Code)
	}
}

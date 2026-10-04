package manage

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"peer-ai-client/internal/config"
)

func splitHostPort(t *testing.T, srv *httptest.Server) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	return host, port
}

func TestAddListDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backends.json")
	s := New(path)
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	b := config.Backend{Name: "ollama", Endpoint: "http://127.0.0.1:11434", Provider: config.ProviderOllama}
	if err := s.Add(b); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(b); err == nil {
		t.Fatal("duplicate add must fail")
	}
	if got := s.List(); len(got) != 1 || got[0].Name != "ollama" {
		t.Fatalf("list = %v", got)
	}
	other := New(path)
	if err := other.Load(); err != nil {
		t.Fatal(err)
	}
	if got := other.List(); len(got) != 1 {
		t.Fatalf("reload list = %v", got)
	}
	if err := s.Delete("missing"); err == nil {
		t.Fatal("delete missing must fail")
	}
	if err := s.Delete("ollama"); err != nil {
		t.Fatal(err)
	}
	if got := s.List(); len(got) != 0 {
		t.Fatalf("list after delete = %v", got)
	}
}

func TestAddRejectsInvalid(t *testing.T) {
	s := New("")
	if err := s.Add(config.Backend{Name: "", Endpoint: "http://127.0.0.1:11434"}); err == nil {
		t.Fatal("invalid backend must fail")
	}
}

func TestSetupSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.yaml")
	s := NewSetup(path)
	if got, err := s.Load(); err != nil || got.KeySet() {
		t.Fatalf("fresh load = %+v, %v; want empty", got, err)
	}
	want := Credentials{CentralHost: "100.64.0.10", CentralPort: 8080, APIKey: "peerai_test"}
	if err := s.Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := NewSetup(path).Load()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("load = %+v, want %+v", got, want)
	}
}

func TestSetupSaveRejectsBadInput(t *testing.T) {
	s := NewSetup("")
	for _, c := range []Credentials{
		{CentralPort: 8080, APIKey: "peerai_x"},
		{CentralHost: "h", CentralPort: 0, APIKey: "peerai_x"},
		{CentralHost: "h", CentralPort: 8080},
	} {
		if err := s.Save(c); err == nil {
			t.Fatalf("Save(%+v) error = nil, want error", c)
		}
	}
}

func TestRegisterUserPostsToCentral(t *testing.T) {
	var gotName, gotPath string
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		var body struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotName = body.Name
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"user_id":"u_1","api_key":"peerai_test","name":"bob"}`))
	}))
	defer central.Close()
	host, port := splitHostPort(t, central)
	reg, err := RegisterUser(context.Background(), central.Client(), host, port, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if reg.APIKey != "peerai_test" || reg.UserID != "u_1" {
		t.Fatalf("registered = %+v", reg)
	}
	if gotPath != "/api/users" || gotName != "bob" {
		t.Fatalf("central got path %q name %q", gotPath, gotName)
	}
}

func TestRegisterUserRejectsEmptyName(t *testing.T) {
	if _, err := RegisterUser(context.Background(), nil, "h", 8080, ""); err == nil {
		t.Fatal("empty name must fail")
	}
}

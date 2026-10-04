package join

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newServer(t *testing.T, binDir string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	New(binDir, nil).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, url, host string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if host != "" {
		req.Host = host
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(body)
}

func TestPageShowsCommandForRequestHost(t *testing.T) {
	srv := newServer(t, t.TempDir())
	resp, body := get(t, srv.URL+"/join", "100.64.1.2:8080")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if want := "curl -fsSL http://100.64.1.2:8080/join.sh | sh"; !strings.Contains(body, want) {
		t.Fatalf("page does not contain %q", want)
	}
}

func TestScriptUsesRequestHost(t *testing.T) {
	srv := newServer(t, t.TempDir())
	_, body := get(t, srv.URL+"/join.sh", "peerai-central:8080")
	if !strings.HasPrefix(body, "#!/bin/sh\n") {
		t.Fatalf("script does not start with a shebang:\n%s", body)
	}
	if want := `CENTRAL="http://peerai-central:8080"`; !strings.Contains(body, want) {
		t.Fatalf("script does not contain %q", want)
	}
}

func TestConfigIsValidYAMLWithCentralAddress(t *testing.T) {
	srv := newServer(t, t.TempDir())
	resp, body := get(t, srv.URL+"/join/peerai-node.yaml", "100.64.1.2:8080")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	for _, want := range []string{
		"  central_host: \"100.64.1.2\"\n  central_port: 8080\n",
		"  listen_port: 9100\n",
		"  - name: \"ollama\"\n",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("config does not contain %q:\n%s", want, body)
		}
	}
}

func TestBinary(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, BinaryName("darwin", "arm64")), []byte("BIN"), 0o755); err != nil {
		t.Fatal(err)
	}
	srv := newServer(t, dir)

	resp, body := get(t, srv.URL+"/join/bin/darwin-arm64", "")
	if resp.StatusCode != http.StatusOK || body != "BIN" {
		t.Fatalf("status %d body %q, want 200 BIN", resp.StatusCode, body)
	}

	resp, _ = get(t, srv.URL+"/join/bin/linux-arm64", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing binary: status %d, want 404", resp.StatusCode)
	}

	resp, _ = get(t, srv.URL+"/join/bin/..%2Fsecret", "")
	if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusNotFound {
		t.Fatalf("path traversal: status %d, want 400 or 404", resp.StatusCode)
	}
}

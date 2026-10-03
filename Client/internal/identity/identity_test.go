package identity

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadOrCreateNodeIDPersistsValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "peerai", "node.id")
	first, err := LoadOrCreateNodeID(path)
	if err != nil {
		t.Fatalf("LoadOrCreateNodeID() error = %v", err)
	}
	second, err := LoadOrCreateNodeID(path)
	if err != nil {
		t.Fatalf("LoadOrCreateNodeID() second error = %v", err)
	}
	if first == "" || first != second {
		t.Fatalf("IDs = %q and %q, want same non-empty value", first, second)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if strings.TrimSpace(string(data)) != first {
		t.Fatalf("file contains %q, want %q", string(data), first)
	}
}

func TestLoadOrCreateNodeIDRejectsEmptyExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node.id")
	if err := os.WriteFile(path, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateNodeID(path); err == nil {
		t.Fatal("LoadOrCreateNodeID() error = nil, want error")
	}
}

func TestResolveTailscaleIPv4(t *testing.T) {
	ip, err := ResolveTailscaleIPv4WithRunner(context.Background(), func(context.Context, string, ...string) ([]byte, error) {
		return []byte("100.64.0.5\n"), nil
	})
	if err != nil {
		t.Fatalf("ResolveTailscaleIPv4WithRunner() error = %v", err)
	}
	if got := ip.String(); got != "100.64.0.5" {
		t.Fatalf("IP = %q, want 100.64.0.5", got)
	}
}

func TestResolveTailscaleIPv4RejectsNonTailscaleAddress(t *testing.T) {
	_, err := ResolveTailscaleIPv4WithRunner(context.Background(), func(context.Context, string, ...string) ([]byte, error) {
		return []byte("192.168.1.2\n"), nil
	})
	if err == nil {
		t.Fatal("ResolveTailscaleIPv4WithRunner() error = nil, want error")
	}
}

func TestResolveTailscaleIPv4ReturnsCommandError(t *testing.T) {
	want := errors.New("not installed")
	_, err := ResolveTailscaleIPv4WithRunner(context.Background(), func(context.Context, string, ...string) ([]byte, error) {
		return nil, want
	})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want wrapped %v", err, want)
	}
}

func TestBuildListenAddr(t *testing.T) {
	addr, err := BuildListenAddr("100.64.0.5", 9100)
	if err != nil {
		t.Fatalf("BuildListenAddr() error = %v", err)
	}
	if addr != "100.64.0.5:9100" {
		t.Fatalf("address = %q, want 100.64.0.5:9100", addr)
	}
}

func TestBuildListenAddrRejectsInvalidValues(t *testing.T) {
	for _, tc := range []struct {
		ip   string
		port int
	}{
		{"127.0.0.1", 9100},
		{"100.64.0.5", 0},
		{"100.64.0.5", 65536},
	} {
		if _, err := BuildListenAddr(tc.ip, tc.port); err == nil {
			t.Errorf("BuildListenAddr(%q, %d) error = nil, want error", tc.ip, tc.port)
		}
	}
}

func TestIsTailscaleIPv4(t *testing.T) {
	if !IsTailscaleIPv4(net.ParseIP("100.127.255.254")) {
		t.Fatal("expected Tailscale address")
	}
	if IsTailscaleIPv4(net.ParseIP("100.128.0.1")) {
		t.Fatal("did not expect address outside Tailscale range")
	}
}

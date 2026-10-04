package main

import (
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func ipNet(s string) *net.IPNet {
	ip, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	n.IP = ip
	return n
}

func TestFindTailscaleIPv4(t *testing.T) {
	tests := []struct {
		name  string
		addrs []net.Addr
		want  string
	}{
		{"none", []net.Addr{ipNet("127.0.0.1/8"), ipNet("192.168.1.20/24")}, ""},
		{"tailscale", []net.Addr{ipNet("192.168.1.20/24"), ipNet("100.101.102.103/32")}, "100.101.102.103"},
		{"ipv6 only", []net.Addr{ipNet("fd7a:115c:a1e0::1/128")}, ""},
		{"outside range", []net.Addr{ipNet("100.128.0.1/32")}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := findTailscaleIPv4(tt.addrs)
			if tt.want == "" {
				if ok {
					t.Fatalf("got %v, want no address", got)
				}
				return
			}
			if !ok || got.String() != tt.want {
				t.Fatalf("got %v (ok=%v), want %s", got, ok, tt.want)
			}
		})
	}
}

func TestHostAddrExplicitHost(t *testing.T) {
	listen, tailscaleIP, err := resolveListenAddr("127.0.0.1:9000")
	if err != nil {
		t.Fatal(err)
	}
	if listen != "127.0.0.1:9000" || tailscaleIP.IsValid() {
		t.Fatalf("got %q %v, want 127.0.0.1:9000 and no Tailscale IP", listen, tailscaleIP)
	}

	listen, tailscaleIP, err = resolveListenAddr("100.64.0.7:8080")
	if err != nil {
		t.Fatal(err)
	}
	if listen != "100.64.0.7:8080" || tailscaleIP.String() != "100.64.0.7" {
		t.Fatalf("got %q %v, want 100.64.0.7:8080 and 100.64.0.7", listen, tailscaleIP)
	}
}

func TestHostAddrInvalid(t *testing.T) {
	if _, _, err := resolveListenAddr("8080"); err == nil {
		t.Fatal("want error for address without port separator")
	}
}

func TestPrintJoinInfo(t *testing.T) {
	var b strings.Builder
	printJoinInfo(&b, netip.MustParseAddr("100.70.1.2"), "8080")
	out := b.String()
	for _, want := range []string{
		"http://100.70.1.2:8080/join",
		"curl -fsSL http://100.70.1.2:8080/join.sh | sh",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not contain %q:\n%s", want, out)
		}
	}
}

func TestLoadEnvFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	body := "# local settings\n\nMOOCH_TEST_URL=postgres://u:p@127.0.0.1:5432/db?sslmode=disable\n" +
		"export MOOCH_TEST_QUOTED=\"a b\"\nMOOCH_TEST_SHELL=from-file\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOOCH_TEST_SHELL", "from-shell")
	for _, k := range []string{"MOOCH_TEST_URL", "MOOCH_TEST_QUOTED"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	if err := loadEnvFile(path); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("MOOCH_TEST_URL"); got != "postgres://u:p@127.0.0.1:5432/db?sslmode=disable" {
		t.Fatalf("url = %q: a value with = must stay whole", got)
	}
	if got := os.Getenv("MOOCH_TEST_QUOTED"); got != "a b" {
		t.Fatalf("quoted = %q", got)
	}
	if got := os.Getenv("MOOCH_TEST_SHELL"); got != "from-shell" {
		t.Fatalf("shell = %q: the shell must win over the file", got)
	}
	if err := loadEnvFile(filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Fatalf("missing file: %v", err)
	}
	if err := os.WriteFile(path, []byte("NOT A PAIR\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := loadEnvFile(path); err == nil || !strings.Contains(err.Error(), ":1:") {
		t.Fatalf("bad line error = %v", err)
	}
}

func TestLoopbackAddr(t *testing.T) {
	cases := map[string]string{
		"100.64.0.7:8080": "127.0.0.1:8080", // Tailscale IPv4: add loopback
		"127.0.0.1:8080":  "",               // already loopback
		"0.0.0.0:8080":    "",               // already all addresses
		"[::]:8080":       "",
		"bad":             "",
	}
	for in, want := range cases {
		got, ok := loopbackAddr(in)
		if got != want || ok != (want != "") {
			t.Errorf("loopbackAddr(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}

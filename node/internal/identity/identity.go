// Package identity resolves the node Tailscale address and stable node ID.
package identity

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const tailscaleCommand = "tailscale"

var tailscaleIPv4Network = &net.IPNet{
	IP:   net.ParseIP("100.64.0.0").To4(),
	Mask: net.CIDRMask(10, 32),
}

// CommandRunner runs a command and returns its standard output.
type CommandRunner func(context.Context, string, ...string) ([]byte, error)

// Identity contains the values that the node sends to central.
type Identity struct {
	NodeID      string
	TailscaleIP string
	ListenAddr  string
}

// DefaultNodeIDPath returns the standard persistent node ID path.
func DefaultNodeIDPath() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("get user config directory: %w", err)
	}
	return filepath.Join(configDir, "mooch", "node.id"), nil
}

// LoadOrCreateNodeID reads a stable ID or creates and persists one.
func LoadOrCreateNodeID(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		var err error
		path, err = DefaultNodeIDPath()
		if err != nil {
			return "", err
		}
	}

	data, err := os.ReadFile(path)
	if err == nil {
		id := strings.TrimSpace(string(data))
		if id == "" {
			return "", fmt.Errorf("node ID file %q is empty", path)
		}
		return id, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read node ID file: %w", err)
	}

	id, err := generateNodeID()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create node ID directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".node.id-*")
	if err != nil {
		return "", fmt.Errorf("create node ID file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return "", fmt.Errorf("set node ID file permissions: %w", err)
	}
	if _, err := tmp.WriteString(id + "\n"); err != nil {
		tmp.Close()
		return "", fmt.Errorf("write node ID file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close node ID file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return "", fmt.Errorf("persist node ID: %w", err)
	}
	return id, nil
}

func generateNodeID() (string, error) {
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		host = "mooch-node"
	}
	host = strings.ReplaceAll(strings.TrimSpace(host), " ", "-")
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate node ID suffix: %w", err)
	}
	return host + "-" + hex.EncodeToString(random), nil
}

// ResolveTailscaleIPv4 gets the node IPv4 from the Tailscale CLI.
func ResolveTailscaleIPv4(ctx context.Context) (net.IP, error) {
	return ResolveTailscaleIPv4WithRunner(ctx, defaultCommandRunner)
}

// ResolveTailscaleIPv4WithRunner resolves a Tailscale IPv4 with runner.
// The runner allows tests to avoid a live Tailscale installation.
func ResolveTailscaleIPv4WithRunner(ctx context.Context, runner CommandRunner) (net.IP, error) {
	if runner == nil {
		return nil, errors.New("Tailscale command runner is nil")
	}
	output, err := runner(ctx, tailscaleCommand, "ip", "-4")
	if err != nil {
		return nil, fmt.Errorf("resolve Tailscale IPv4: %w", err)
	}
	for _, line := range strings.Fields(string(output)) {
		ip := net.ParseIP(line)
		if IsTailscaleIPv4(ip) {
			return ip.To4(), nil
		}
	}
	return nil, fmt.Errorf("Tailscale command returned no IPv4 address")
}

func defaultCommandRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// IsTailscaleIPv4 reports whether ip is in Tailscale's IPv4 CGNAT range.
func IsTailscaleIPv4(ip net.IP) bool {
	ip = ip.To4()
	return ip != nil && tailscaleIPv4Network.Contains(ip)
}

// BuildListenAddr returns the Tailscale address for the node listener.
func BuildListenAddr(tailscaleIP string, port int) (string, error) {
	ip := net.ParseIP(strings.TrimSpace(tailscaleIP))
	if !IsTailscaleIPv4(ip) {
		return "", fmt.Errorf("invalid Tailscale IPv4 address %q", tailscaleIP)
	}
	if port < 1 || port > 65535 {
		return "", fmt.Errorf("invalid listen port %d", port)
	}
	return net.JoinHostPort(ip.To4().String(), fmt.Sprintf("%d", port)), nil
}

package manage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// Credentials is the one-time setup of a node.
type Credentials struct {
	CentralHost string `yaml:"central_host" json:"central_host"`
	CentralPort int    `yaml:"central_port" json:"central_port"`
	APIKey      string `yaml:"api_key" json:"api_key"`
}

// Validate checks that credentials can reach central.
func (c Credentials) Validate() error {
	if strings.TrimSpace(c.CentralHost) == "" {
		return errors.New("central_host is required")
	}
	if c.CentralPort < 1 || c.CentralPort > 65535 {
		return errors.New("central_port must be between 1 and 65535")
	}
	if strings.TrimSpace(c.APIKey) == "" {
		return errors.New("api_key is required")
	}
	return nil
}

// KeySet reports when a key is stored. The status endpoint uses it
// instead of returning the key to the browser.
func (c Credentials) KeySet() bool { return strings.TrimSpace(c.APIKey) != "" }

// SetupStore persists Credentials in one YAML file. It is safe for use.
type SetupStore struct {
	mu   sync.Mutex
	path string
}

// NewSetup returns a SetupStore. An empty path means memory only.
func NewSetup(path string) *SetupStore { return &SetupStore{path: path} }

// DefaultCredentialsPath returns the credentials path for a config file.
// PEERAI_CREDENTIALS_FILE wins. Otherwise credentials.yaml sits next to
// the config file, so dev checkouts keep secrets out of the repo config.
func DefaultCredentialsPath(configPath string) (string, error) {
	if p := strings.TrimSpace(os.Getenv("PEERAI_CREDENTIALS_FILE")); p != "" {
		return p, nil
	}
	if strings.TrimSpace(configPath) == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, "peerai", "credentials.yaml"), nil
	}
	abs, err := filepath.Abs(configPath)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(abs), "credentials.yaml"), nil
}

// Load reads the file. A missing file means zero credentials.
func (s *SetupStore) Load() (Credentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path == "" {
		return Credentials{}, nil
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Credentials{}, nil
		}
		return Credentials{}, fmt.Errorf("read credentials: %w", err)
	}
	var c Credentials
	if err := yaml.Unmarshal(data, &c); err != nil {
		return Credentials{}, fmt.Errorf("parse credentials: %w", err)
	}
	return c, nil
}

// Save validates and writes the file with owner-only permissions.
func (s *SetupStore) Save(c Credentials) error {
	c.CentralHost = strings.TrimSpace(c.CentralHost)
	c.APIKey = strings.TrimSpace(c.APIKey)
	if err := c.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path == "" {
		return errors.New("credentials path is not set")
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o600)
}

// Registered is the answer of central POST /api/users.
type Registered struct {
	UserID string `json:"user_id"`
	APIKey string `json:"api_key"`
	Name   string `json:"name"`
}

// RegisterUser creates a user on central and returns the key.
// The node website calls this, so the user never touches curl.
// Central is a normal server. This is a normal POST.
func RegisterUser(ctx context.Context, client *http.Client, host string, port int, name string) (Registered, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Registered{}, errors.New("name is required")
	}
	if host == "" || port < 1 || port > 65535 {
		return Registered{}, errors.New("central address is invalid")
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	body, _ := json.Marshal(map[string]string{"name": name})
	url := "http://" + net.JoinHostPort(host, strconv.Itoa(port)) + "/api/users"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return Registered{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return Registered{}, fmt.Errorf("reach central at %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return Registered{}, fmt.Errorf("central returned HTTP %d", resp.StatusCode)
	}
	var out Registered
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Registered{}, fmt.Errorf("decode central answer: %w", err)
	}
	if out.APIKey == "" || out.UserID == "" {
		return Registered{}, errors.New("central answer misses user_id or api_key")
	}
	return out, nil
}

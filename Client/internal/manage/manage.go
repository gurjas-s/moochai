// Package manage keeps the state of the local node website.
//
// The user registers with central and adds backends in the browser.
// The node stores backends in backends.json and the one-time setup in
// credentials.yaml. The user never edits YAML by hand.
package manage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"peer-ai-client/internal/config"
)

// Store holds backends added in the local website. It is safe for use.
type Store struct {
	mu       sync.Mutex
	path     string
	backends map[string]config.Backend
}

// New returns a Store. An empty path means memory only.
func New(path string) *Store {
	return &Store{path: path, backends: map[string]config.Backend{}}
}

// DefaultPath returns ~/.config/peerai/backends.json.
// PEERAI_MANAGED_FILE overrides it.
func DefaultPath() (string, error) {
	if p := strings.TrimSpace(os.Getenv("PEERAI_MANAGED_FILE")); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "peerai", "backends.json"), nil
}

// Load reads the JSON file. A missing file means an empty list.
func (s *Store) Load() error {
	if s.path == "" {
		return nil
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read managed backends: %w", err)
	}
	var list []config.Backend
	if err := json.Unmarshal(data, &list); err != nil {
		return fmt.Errorf("parse managed backends: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := map[string]config.Backend{}
	for i, b := range list {
		if err := b.Validate(); err != nil {
			return fmt.Errorf("managed backends[%d]: %w", i, err)
		}
		if _, dup := next[b.Name]; dup {
			return fmt.Errorf("managed backend %q is duplicated", b.Name)
		}
		next[b.Name] = b
	}
	s.backends = next
	return nil
}

// saveLocked writes the file. The caller holds mu.
func (s *Store) saveLocked() error {
	if s.path == "" {
		return nil
	}
	list := make([]config.Backend, 0, len(s.backends))
	for _, b := range s.backends {
		list = append(list, b)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o600)
}

// List returns managed backends sorted by name.
func (s *Store) List() []config.Backend {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]config.Backend, 0, len(s.backends))
	for _, b := range s.backends {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Add validates and stores one backend. A name collision fails.
func (s *Store) Add(b config.Backend) error {
	if err := b.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.backends[b.Name]; ok {
		return fmt.Errorf("backend %q already exists", b.Name)
	}
	s.backends[b.Name] = b
	return s.saveLocked()
}

// Delete removes one backend by name.
func (s *Store) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.backends[name]; !ok {
		return fmt.Errorf("backend %q not found", name)
	}
	delete(s.backends, name)
	return s.saveLocked()
}

// Merge returns file backends plus managed ones.
// Managed entries win on name collision.
func (s *Store) Merge(file []config.Backend) []config.Backend {
	managed := s.List()
	out := make([]config.Backend, 0, len(file)+len(managed))
	for _, b := range file {
		out = append(out, b)
	}
	for _, b := range managed {
		found := false
		for i, cur := range out {
			if cur.Name == b.Name {
				out[i] = b
				found = true
				break
			}
		}
		if !found {
			out = append(out, b)
		}
	}
	return out
}

// Package auth keeps users and groups for central.
// Users self-register and get a long-lived API key.
// Nodes and tools send the key as Authorization: Bearer.
// Groups share nodes in private mode.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// User is one registered identity.
type User struct {
	ID        string    `json:"user_id"`
	Name      string    `json:"name"`
	APIKey    string    `json:"-"`
	CreatedAt time.Time `json:"created_at"`
}

// Group is one private sharing set.
type Group struct {
	ID        string          `json:"group_id"`
	Name      string          `json:"name"`
	OwnerID   string          `json:"owner_id"`
	Members   map[string]bool `json:"-"`
	CreatedAt time.Time       `json:"created_at"`
}

// Member is one public group member for UI responses.
type Member struct {
	UserID string `json:"user_id"`
	Name   string `json:"name"`
}

// GroupView is the JSON shape the UI reads.
type GroupView struct {
	GroupID   string    `json:"group_id"`
	Name      string    `json:"name"`
	OwnerID   string    `json:"owner_id"`
	Members   []Member  `json:"members"`
	CreatedAt time.Time `json:"created_at"`
}

// Store is safe for concurrent use. Data lives in memory only.
type Store struct {
	mu     sync.Mutex
	users  map[string]User
	names  map[string]string
	keys   map[string]string
	groups map[string]*Group
}

// New returns an empty Store.
func New() *Store {
	return &Store{
		users:  map[string]User{},
		names:  map[string]string{},
		keys:   map[string]string{},
		groups: map[string]*Group{},
	}
}

// CreateUser adds a user with a fresh API key. Names must be unique.
func (s *Store) CreateUser(name string) (User, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return User{}, errors.New("name is required")
	}
	if len(name) > 64 {
		return User{}, errors.New("name must be 64 characters or less")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, taken := s.names[name]; taken {
		return User{}, errors.New("name is already taken")
	}
	u := User{
		ID:        "u_" + hexID(8),
		Name:      name,
		APIKey:    "peerai_" + hexID(32),
		CreatedAt: time.Now(),
	}
	s.users[u.ID] = u
	s.names[name] = u.ID
	s.keys[u.APIKey] = u.ID
	return u, nil
}

// GetUser returns a user by ID.
func (s *Store) GetUser(id string) (User, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[id]
	return u, ok
}

// Authenticate returns the user for a Bearer key. It returns false
// when the header is missing or the key is unknown.
func (s *Store) Authenticate(r *http.Request) (User, bool) {
	key := bearer(r)
	if key == "" {
		return User{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for stored, id := range s.keys {
		if subtle.ConstantTimeCompare([]byte(stored), []byte(key)) == 1 {
			u, ok := s.users[id]
			return u, ok
		}
	}
	return User{}, false
}

// CreateGroup adds a group. The owner becomes a member.
func (s *Store) CreateGroup(owner User, name string) (GroupView, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return GroupView{}, errors.New("name is required")
	}
	if len(name) > 64 {
		return GroupView{}, errors.New("name must be 64 characters or less")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	g := &Group{
		ID:        "g_" + hexID(8),
		Name:      name,
		OwnerID:   owner.ID,
		Members:   map[string]bool{owner.ID: true},
		CreatedAt: time.Now(),
	}
	s.groups[g.ID] = g
	return s.viewLocked(g), nil
}

// ListGroups returns groups the user belongs to, sorted by ID.
func (s *Store) ListGroups(userID string) []GroupView {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []GroupView{}
	for _, g := range s.groups {
		if g.Members[userID] {
			out = append(out, s.viewLocked(g))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GroupID < out[j].GroupID })
	return out
}

// GetGroup returns a group view when the user is a member.
func (s *Store) GetGroup(callerID, groupID string) (GroupView, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.groups[groupID]
	if !ok || !g.Members[callerID] {
		return GroupView{}, false
	}
	return s.viewLocked(g), true
}

// GroupExists reports when a group ID exists.
func (s *Store) GroupExists(groupID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.groups[groupID]
	return ok
}

// IsMember reports group membership.
func (s *Store) IsMember(groupID, userID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.groups[groupID]
	return ok && g.Members[userID]
}

// GroupIDsFor returns the set of groups the user belongs to.
func (s *Store) GroupIDsFor(userID string) map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]bool{}
	for id, g := range s.groups {
		if g.Members[userID] {
			out[id] = true
		}
	}
	return out
}

// AddMember adds a user to a group. Only the owner can add.
func (s *Store) AddMember(caller User, groupID, userID string) (GroupView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.groups[groupID]
	if !ok {
		return GroupView{}, errors.New("group not found")
	}
	if g.OwnerID != caller.ID {
		return GroupView{}, errors.New("only the group owner can add members")
	}
	if _, ok := s.users[userID]; !ok {
		return GroupView{}, errors.New("user not found")
	}
	g.Members[userID] = true
	return s.viewLocked(g), nil
}

// RemoveMember removes a user from a group. The owner can remove
// any member except self. A member can remove self to leave.
// The owner cannot leave the group.
func (s *Store) RemoveMember(caller User, groupID, targetID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.groups[groupID]
	if !ok {
		return errors.New("group not found")
	}
	if !g.Members[targetID] {
		return errors.New("user is not a member")
	}
	if caller.ID != g.OwnerID && caller.ID != targetID {
		return errors.New("only the group owner can remove other members")
	}
	if targetID == g.OwnerID {
		return errors.New("the group owner cannot leave the group")
	}
	delete(g.Members, targetID)
	return nil
}

func (s *Store) viewLocked(g *Group) GroupView {
	members := make([]Member, 0, len(g.Members))
	for id := range g.Members {
		name := id
		if u, ok := s.users[id]; ok {
			name = u.Name
		}
		members = append(members, Member{UserID: id, Name: name})
	}
	sort.Slice(members, func(i, j int) bool { return members[i].UserID < members[j].UserID })
	return GroupView{
		GroupID:   g.ID,
		Name:      g.Name,
		OwnerID:   g.OwnerID,
		Members:   members,
		CreatedAt: g.CreatedAt,
	}
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	parts := strings.SplitN(h, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

func hexID(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

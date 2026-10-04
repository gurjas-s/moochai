// Package auth keeps users, API keys, and groups in memory.
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

	"peerai-serv/internal/respond"
)

var (
	ErrGroupNotFound   = errors.New("group not found")
	ErrNotMember       = errors.New("user is not a member")
	ErrOwnerOnlyAdd    = errors.New("only the group owner can add members")
	ErrOwnerOnlyRemove = errors.New("only the group owner can remove other members")
)

type User struct {
	ID, Name, APIKey string
}

type Member struct {
	UserID string `json:"user_id"`
	Name   string `json:"name"`
}

type Group struct {
	GroupID   string    `json:"group_id"`
	Name      string    `json:"name"`
	OwnerID   string    `json:"owner_id"`
	Members   []Member  `json:"members"`
	CreatedAt time.Time `json:"created_at"`
}

type group struct {
	id        string
	name      string
	ownerID   string
	members   map[string]bool
	createdAt time.Time
}

type Store struct {
	mu      sync.Mutex
	users   map[string]User
	names   map[string]bool
	keyToID map[string]string
	groups  map[string]*group
}

func New() *Store {
	return &Store{
		users:   map[string]User{},
		names:   map[string]bool{},
		keyToID: map[string]string{},
		groups:  map[string]*group{},
	}
}

func (s *Store) CreateUser(name string) (User, error) {
	name, err := cleanName(name)
	if err != nil {
		return User{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.names[name] {
		return User{}, errors.New("name is already taken")
	}
	u := User{ID: "u_" + randomHex(8), Name: name, APIKey: "peerai_" + randomHex(32)}
	s.users[u.ID] = u
	s.names[name] = true
	s.keyToID[u.APIKey] = u.ID
	return u, nil
}

func (s *Store) Authenticate(r *http.Request) (User, bool) {
	key := bearerToken(r)
	if key == "" {
		return User{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for stored, id := range s.keyToID {
		if subtle.ConstantTimeCompare([]byte(stored), []byte(key)) == 1 {
			u, ok := s.users[id]
			return u, ok
		}
	}
	return User{}, false
}

func (s *Store) RequireUser(w http.ResponseWriter, r *http.Request) (User, bool) {
	u, ok := s.Authenticate(r)
	if !ok {
		respond.Error(w, http.StatusUnauthorized, "missing or invalid API key")
	}
	return u, ok
}

func (s *Store) CreateGroup(owner User, name string) (Group, error) {
	name, err := cleanName(name)
	if err != nil {
		return Group{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	g := &group{
		id:        "g_" + randomHex(8),
		name:      name,
		ownerID:   owner.ID,
		members:   map[string]bool{owner.ID: true},
		createdAt: time.Now(),
	}
	s.groups[g.id] = g
	return s.toGroupLocked(g), nil
}

func (s *Store) ListGroups(userID string) []Group {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Group{}
	for _, g := range s.groups {
		if g.members[userID] {
			out = append(out, s.toGroupLocked(g))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GroupID < out[j].GroupID })
	return out
}

func (s *Store) GetGroup(callerID, groupID string) (Group, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.groups[groupID]
	if !ok || !g.members[callerID] {
		return Group{}, false
	}
	return s.toGroupLocked(g), true
}

func (s *Store) IsMember(groupID, userID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.groups[groupID]
	return ok && g.members[userID]
}

func (s *Store) GroupIDsFor(userID string) map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]bool{}
	for id, g := range s.groups {
		if g.members[userID] {
			out[id] = true
		}
	}
	return out
}

func (s *Store) AddMember(caller User, groupID, userID string) (Group, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.groups[groupID]
	if !ok {
		return Group{}, ErrGroupNotFound
	}
	if g.ownerID != caller.ID {
		return Group{}, ErrOwnerOnlyAdd
	}
	if _, ok := s.users[userID]; !ok {
		return Group{}, errors.New("user not found")
	}
	g.members[userID] = true
	return s.toGroupLocked(g), nil
}

// The owner can remove any member except self. A member can remove only self.
func (s *Store) RemoveMember(caller User, groupID, targetID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.groups[groupID]
	if !ok {
		return ErrGroupNotFound
	}
	if !g.members[targetID] {
		return ErrNotMember
	}
	if caller.ID != g.ownerID && caller.ID != targetID {
		return ErrOwnerOnlyRemove
	}
	if targetID == g.ownerID {
		return errors.New("the group owner cannot leave the group")
	}
	delete(g.members, targetID)
	return nil
}

func (s *Store) toGroupLocked(g *group) Group {
	members := make([]Member, 0, len(g.members))
	for id := range g.members {
		name := id
		if u, ok := s.users[id]; ok {
			name = u.Name
		}
		members = append(members, Member{UserID: id, Name: name})
	}
	sort.Slice(members, func(i, j int) bool { return members[i].UserID < members[j].UserID })
	return Group{
		GroupID:   g.id,
		Name:      g.name,
		OwnerID:   g.ownerID,
		Members:   members,
		CreatedAt: g.createdAt,
	}
}

func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("name is required")
	}
	if len(name) > 64 {
		return "", errors.New("name must be 64 characters or less")
	}
	return name, nil
}

func bearerToken(r *http.Request) string {
	scheme, key, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "bearer") {
		return ""
	}
	return strings.TrimSpace(key)
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

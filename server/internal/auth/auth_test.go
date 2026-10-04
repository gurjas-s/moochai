package auth

import (
	"net/http/httptest"
	"testing"
)

func TestCreateUserAndAuthenticate(t *testing.T) {
	s := New()
	u, err := s.CreateUser("alice")
	if err != nil {
		t.Fatal(err)
	}
	if u.ID == "" || u.APIKey == "" {
		t.Fatal("user needs ID and key")
	}
	if _, err := s.CreateUser("alice"); err == nil {
		t.Fatal("duplicate name must fail")
	}
	if _, err := s.CreateUser(""); err == nil {
		t.Fatal("empty name must fail")
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer "+u.APIKey)
	got, ok := s.Authenticate(r)
	if !ok || got.ID != u.ID {
		t.Fatal("valid key must authenticate")
	}
	bad := httptest.NewRequest("GET", "/", nil)
	bad.Header.Set("Authorization", "Bearer wrong")
	if _, ok := s.Authenticate(bad); ok {
		t.Fatal("wrong key must not authenticate")
	}
	none := httptest.NewRequest("GET", "/", nil)
	if _, ok := s.Authenticate(none); ok {
		t.Fatal("missing key must not authenticate")
	}
}

func TestGroups(t *testing.T) {
	s := New()
	a, _ := s.CreateUser("alice")
	b, _ := s.CreateUser("bob")
	c, _ := s.CreateUser("carol")

	g, err := s.CreateGroup(a, "lab")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.GetGroup(b.ID, g.GroupID); ok {
		t.Fatal("non-member must not read group")
	}
	if _, err := s.AddMember(b, g.GroupID, c.ID); err == nil {
		t.Fatal("non-owner must not add members")
	}
	if _, err := s.AddMember(a, g.GroupID, "u_missing"); err == nil {
		t.Fatal("unknown user must not join")
	}
	if _, err := s.AddMember(a, g.GroupID, b.ID); err != nil {
		t.Fatal(err)
	}
	if !s.IsMember(g.GroupID, b.ID) {
		t.Fatal("bob must be a member")
	}
	if got := s.ListGroups(b.ID); len(got) != 1 {
		t.Fatalf("bob has %d groups, want 1", len(got))
	}
	if ids := s.GroupIDsFor(b.ID); !ids[g.GroupID] {
		t.Fatal("group set must hold the group")
	}
	// Member can leave.
	if err := s.RemoveMember(b, g.GroupID, b.ID); err != nil {
		t.Fatal(err)
	}
	// Owner cannot leave.
	if err := s.RemoveMember(a, g.GroupID, a.ID); err == nil {
		t.Fatal("owner must not leave")
	}
	// Non-owner cannot remove others.
	if _, err := s.AddMember(a, g.GroupID, b.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveMember(c, g.GroupID, b.ID); err == nil {
		t.Fatal("outsider must not remove members")
	}
}

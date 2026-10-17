package server

import (
	"testing"

	"github.com/benjaminwood/eldar-os/supervisor/api"
)

func names(n ...string) api.Creds { return api.Creds{Names: n} }
func minutes(m int) *int          { return &m }

func TestGrantMatching(t *testing.T) {
	g := NewGrants(NewNotifier())
	scoped := g.Add(api.GrantInput{Session: "s1", Profile: "work", Credentials: names("GITHUB_TOKEN"), Aws: names(), Minutes: minutes(10)})
	g.Add(api.GrantInput{Session: "*", Profile: "personal", Credentials: api.AnyCreds, Aws: names("dev")})

	cases := []struct {
		name string
		r    GrantRequest
		want int // grant ID, 0 = none
	}{
		{"verified session, subset", GrantRequest{Target: "eldar", SessionID: "s1", Verified: true, Profile: "work", Creds: []string{"GITHUB_TOKEN"}}, scoped.ID},
		{"unverified session can't borrow", GrantRequest{Target: "eldar", SessionID: "s1", Verified: false, Profile: "work", Creds: []string{"GITHUB_TOKEN"}}, 0},
		{"other session", GrantRequest{Target: "eldar", SessionID: "s2", Verified: true, Profile: "work", Creds: []string{"GITHUB_TOKEN"}}, 0},
		{"not a subset", GrantRequest{Target: "eldar", SessionID: "s1", Verified: true, Profile: "work", Creds: []string{"GITHUB_TOKEN", "OTHER"}}, 0},
		{"no credentials is a subset", GrantRequest{Target: "eldar", SessionID: "s1", Verified: true, Profile: "work", Creds: []string{}}, scoped.ID},
		{"whole profile needs *", GrantRequest{Target: "eldar", SessionID: "s1", Verified: true, Profile: "work", Creds: nil}, 0},
		{"aws needs coverage", GrantRequest{Target: "eldar", SessionID: "s1", Verified: true, Profile: "work", Creds: []string{}, AwsProfile: "dev"}, 0},
		{"global grant, whole profile", GrantRequest{Target: "eldar", SessionID: "anyone", Profile: "personal", Creds: nil}, 2},
		{"global grant, its aws", GrantRequest{Target: "eldar", Profile: "personal", AwsProfile: "dev"}, 2},
		{"global grant, other aws", GrantRequest{Target: "eldar", Profile: "personal", AwsProfile: "prod"}, 0},
		{"wrong target", GrantRequest{Target: "mac", Profile: "personal"}, 0},
	}
	for _, c := range cases {
		got := g.Match(c.r)
		id := 0
		if got != nil {
			id = got.ID
		}
		if id != c.want {
			t.Errorf("%s: matched grant %d, want %d", c.name, id, c.want)
		}
	}
}

func TestGrantSessionSpecificFirst(t *testing.T) {
	g := NewGrants(NewNotifier())
	global := g.Add(api.GrantInput{Session: "*", Profile: "*", Credentials: api.AnyCreds})
	specific := g.Add(api.GrantInput{Session: "s1", Profile: "*", Credentials: api.AnyCreds})
	if m := g.Match(GrantRequest{Target: "eldar", SessionID: "s1", Verified: true, Profile: "p"}); m == nil || m.ID != specific.ID {
		t.Fatalf("want the session's grant first, got %+v", m)
	}
	if m := g.Match(GrantRequest{Target: "eldar", SessionID: "s9", Verified: true, Profile: "p"}); m == nil || m.ID != global.ID {
		t.Fatalf("want the global grant, got %+v", m)
	}
}

func TestGrantDefaultsAndExpiry(t *testing.T) {
	g := NewGrants(NewNotifier())
	gr := g.Add(api.GrantInput{Target: "mac", Profile: "", Credentials: api.AnyCreds, Aws: api.AnyCreds})
	if gr.Session != "*" || gr.Profile != "*" || gr.ExpiresAt != nil {
		t.Fatalf("defaults: %+v", gr)
	}
	if gr.Aws.Any || len(gr.Aws.Names) != 0 {
		t.Fatalf("mac grants carry no AWS: %+v", gr.Aws)
	}
	zero := 0
	short := g.Add(api.GrantInput{Profile: "*", Credentials: api.AnyCreds, Minutes: &zero})
	if short.ExpiresAt == nil {
		t.Fatal("minutes 0 should still expire (clamped to 1)")
	}
	if n := g.Clear(); n != 2 || len(g.List()) != 0 {
		t.Fatalf("Clear: n=%d list=%v", n, g.List())
	}
}

func TestSessionsPinning(t *testing.T) {
	s := NewSessions(NewNotifier())
	if s.Touch("", "k", TouchMeta{}) {
		t.Fatal("no ID can't be verified")
	}
	if !s.Touch("s1", "key-a", TouchMeta{Action: "first\nsecond"}) {
		t.Fatal("first key pins and verifies")
	}
	if s.Touch("s1", "key-b", TouchMeta{Cwd: "/evil"}) {
		t.Fatal("a different key must not verify")
	}
	if !s.Touch("s1", "key-a", TouchMeta{}) {
		t.Fatal("pinned key verifies")
	}
	if s.Touch("s1", "", TouchMeta{}) {
		t.Fatal("no key doesn't verify")
	}
	l := s.List()
	if len(l) != 1 || l[0].KeyMismatches != 1 || l[0].Cwd == "/evil" || l[0].LastAction != "first" || !l[0].Pinned {
		t.Fatalf("session info: %+v", l)
	}
	s.Unpin("s1")
	if !s.Touch("s1", "key-b", TouchMeta{}) {
		t.Fatal("after unpin, the next key pins")
	}
}

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/benjaminwood/eldar-os/supervisor/api"
)

func get(t *testing.T, srv *httptest.Server, path string) []byte {
	t.Helper()
	res, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 {
		t.Fatalf("%s: %d %s", path, res.StatusCode, b)
	}
	return b
}

// The UIs decode what the demo serves into the same types, so a change to
// the admin API that the demo hasn't caught up with shows up here.
func TestDemoState(t *testing.T) {
	srv := httptest.NewServer(newDemo().handler())
	defer srv.Close()
	var st api.State
	if err := json.Unmarshal(get(t, srv, "/admin/state"), &st); err != nil {
		t.Fatal(err)
	}
	if len(st.Pending) == 0 || len(st.Sessions) == 0 || len(st.Herdr.Spaces) == 0 || len(st.Hosts) == 0 {
		t.Fatalf("the screenshots need pending approvals, sessions, spaces and a host: %+v", st)
	}
	for _, p := range st.Pending {
		if p.ExpiresAt <= st.Now {
			t.Errorf("approval #%d has already expired", p.ID)
		}
	}
}

var (
	ipv4  = regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)
	email = regexp.MustCompile(`[\w.+-]+@[\w-]+(\.[\w-]+)+`)
)

// Screenshots are published: the demo must stay made up. Addresses come
// from the documentation ranges (RFC 5737), email from example domains.
func TestDemoDataIsMadeUp(t *testing.T) {
	srv := httptest.NewServer(newDemo().handler())
	defer srv.Close()
	var all strings.Builder
	for _, p := range []string{"/admin/state", "/admin/chat", "/admin/audit", "/admin/herdr", "/admin/aws"} {
		all.Write(get(t, srv, p))
	}
	doc := []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24")}
	for _, s := range ipv4.FindAllString(all.String(), -1) {
		a, err := netip.ParseAddr(s)
		if err != nil {
			continue
		}
		ok := false
		for _, p := range doc {
			ok = ok || p.Contains(a)
		}
		if !ok {
			t.Errorf("%s isn't a documentation address (192.0.2/24, 198.51.100/24, 203.0.113/24)", s)
		}
	}
	for _, e := range email.FindAllString(all.String(), -1) {
		if d := e[strings.LastIndex(e, "@")+1:]; !regexp.MustCompile(`^example\.(com|org|net)$`).MatchString(d) {
			t.Errorf("%s isn't at an example domain", e)
		}
	}
}

// The takeover view's terminal: a full screen at the asked size, redrawn on
// resize.
func TestDemoTerminal(t *testing.T) {
	srv := httptest.NewServer(newDemo().handler())
	defer srv.Close()
	for _, size := range [][2]int{{46, 30}, {140, 40}} {
		s := session(size[0], size[1], 0, 0)
		lines := strings.Split(strings.TrimPrefix(s, "\x1b[2J\x1b[H\x1b[?25l"), "\r\n")
		if len(lines) != size[1] {
			t.Errorf("%dx%d: %d lines", size[0], size[1], len(lines))
		}
		plain := regexp.MustCompile("\x1b\\[[0-9;?]*[a-zA-Z]")
		for i, l := range lines {
			if n := utf8.RuneCountInString(plain.ReplaceAllString(l, "")); n > size[0] {
				t.Errorf("%dx%d: line %d is %d wide: %q", size[0], size[1], i, n, l)
			}
		}
	}
}

// The promo shows these behaviors, so they must be the real supervisor's:
// a decision resolves its approval and is audited; a grant covers its own
// session's matching requests and nothing else (no other session, no AWS it
// doesn't name).
func TestDemoSemantics(t *testing.T) {
	d := newDemo()
	if !d.decide(42, api.Approve) || d.decide(42, api.Approve) {
		t.Fatal("an approval resolves once")
	}
	if last := d.audit[len(d.audit)-1]; last.RequestID != 42 || last.DecisionReason != "Approved by Ben" || last.Target != "mac" {
		t.Fatalf("audit: %+v", last)
	}
	work := []string{"GITHUB_TOKEN"}
	gh := request{Session: sBilling, Command: "gh pr create --fill", Credentials: work}
	if _, ran := d.file(gh); ran {
		t.Fatal("without a grant, a request waits for Ben")
	}
	min := 60.0
	g := d.addGrant(grantBody{Session: sBilling, Profile: "work", Credentials: &api.Creds{Names: work}, Minutes: &min}, "test")
	if _, ran := d.file(gh); !ran {
		t.Fatal("the grant covers its session's request")
	}
	if last := d.audit[len(d.audit)-1]; !strings.HasPrefix(last.DecisionReason, fmt.Sprintf("Bypass grant #%d", g.ID)) {
		t.Fatalf("audit: %+v", last)
	}
	for _, r := range []request{
		{Session: sDocs, Command: "gh pr create --fill", Credentials: work},
		{Session: sBilling, Command: "sentry-cli releases new", Credentials: []string{"SENTRY_TOKEN"}},
		{Session: sBilling, Command: "aws logs tail /ecs/api", AwsProfile: "staging.ReadOnly"},
	} {
		if _, ran := d.file(r); ran {
			t.Errorf("the grant must not cover %+v", r)
		}
	}
	if st, _ := d.story(); st != 0 {
		t.Fatal("the terminal waits on #41 first")
	}
	d.decide(41, api.Approve)
	if st, c := d.story(); st != 1 || c != 43 {
		t.Fatalf("story %d %d", st, c)
	}
	d.decide(43, api.Approve)
	if st, _ := d.story(); st != 2 {
		t.Fatal("story done")
	}
}

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"regexp"
	"strings"
	"testing"

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
	srv := httptest.NewServer(handler())
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
	srv := httptest.NewServer(handler())
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

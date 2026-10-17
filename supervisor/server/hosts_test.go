package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/benjaminwood/eldar-os/supervisor/api"
)

func TestHostAllow(t *testing.T) {
	p := filepath.Join(t.TempDir(), "host-allow.json")
	a := NewHostAllow(p)
	if a.Add("mac", "*") == nil || a.Add("mac", " * ") == nil {
		t.Fatal("a match-everything pattern must be refused")
	}
	if err := a.Add("mac", "brew *"); err != nil {
		t.Fatal(err)
	}
	if a.Match("mac", "brew list") != "brew *" || a.Match("nas", "brew list") != "" || a.Match("mac", "brewx") != "" {
		t.Fatal("lists are per host, globs anchored")
	}
	if got := NewHostAllow(p).List("mac"); len(got) != 1 || got[0] != "brew *" {
		t.Fatalf("persisted: %v", got)
	}
	if !a.Remove("mac", "brew *") || a.Remove("mac", "brew *") {
		t.Fatal("remove")
	}
}

func TestHostScript(t *testing.T) {
	cmd := `echo "$TOKEN" | grep 'x'`
	s := hostScript(cmd, "~/code app", map[string]string{"TOKEN": "a'b", "bad-name": "x"}, 90*time.Second)
	if !strings.Contains(s, base64.StdEncoding.EncodeToString([]byte(cmd))) || strings.Contains(s, `echo "$TOKEN"`) {
		t.Fatal("the command travels base64-encoded only")
	}
	if !strings.Contains(s, `export TOKEN='a'\''b'`) || strings.Contains(s, "bad-name") {
		t.Fatalf("exports: %s", s)
	}
	if !strings.Contains(s, `cd -- "$HOME"/'code app' || exit 97`) || !strings.Contains(s, "sleep 90;") {
		t.Fatalf("cwd/timeout: %s", s)
	}
}

func TestLoadHosts(t *testing.T) {
	dir := t.TempDir()
	write := func(v any) string {
		p := filepath.Join(dir, "hosts.json")
		b, _ := json.Marshal(v)
		os.WriteFile(p, b, 0o600)
		return p
	}
	for _, bad := range []string{"eldar", "browser", "Mac", "a b", ""} {
		if _, err := LoadHosts(write([]map[string]string{{"name": bad, "target": "u@h", "hostKey": "ssh-ed25519 K"}}), dir, dir); err == nil {
			t.Fatalf("host name %q accepted", bad)
		}
	}
	hosts, err := LoadHosts(write([]map[string]string{{"name": "mac", "label": "Ben's Mac", "target": "ben@10.0.0.9", "hostKey": "ssh-ed25519 KEY", "shell": "/bin/zsh -l -s"}}), dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	h := hosts["mac"]
	kh, _ := os.ReadFile(h.KnownHosts)
	if h.Label != "Ben's Mac" || h.Shell != "/bin/zsh -l -s" || string(kh) != "10.0.0.9 ssh-ed25519 KEY\n" || h.Configured() {
		t.Fatalf("host %+v, known_hosts %q", h, kh)
	}
	os.WriteFile(filepath.Join(dir, "mac"), []byte("k"), 0o600)
	if !h.Configured() {
		t.Fatal("configured once its key exists")
	}
	if none, err := LoadHosts(filepath.Join(dir, "missing.json"), dir, dir); err != nil || len(none) != 0 {
		t.Fatal("no hosts file = no hosts")
	}
}

func TestHostExecDecisions(t *testing.T) {
	s, _ := newTest(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "mac"), []byte("k"), 0o600)
	s.cfg.Hosts = map[string]*RemoteHost{"mac": {Name: "mac", Label: "Ben's Mac", Target: "ben@mac", KeyPath: filepath.Join(dir, "mac")},
		"nas": {Name: "nas", Label: "NAS", Target: "ben@nas", KeyPath: filepath.Join(dir, "nas")}}
	s.hostAllow = NewHostAllow(filepath.Join(dir, "allow.json"))
	ctx := context.Background()
	msg := func(r response) string { m, _ := body(t, r)["error"].(string); return m }

	if r := s.HandleExec(ctx, ExecRequest{Target: "printer", Script: "ls"}); r.status != 400 || !strings.Contains(msg(r), "mac, nas") {
		t.Fatalf("unknown target: %v", r.body)
	}
	if r := s.HandleExec(ctx, ExecRequest{Target: "nas", Script: "ls"}); r.status != 400 || !strings.Contains(msg(r), "no key") {
		t.Fatalf("host without a key: %v", r.body)
	}
	if r := s.HandleExec(ctx, ExecRequest{Target: "mac", Script: "denied thing", Credentials: creds()}); r.status != 403 || body(t, r)["outcome"] != string(OutBlockedPolicy) {
		t.Fatalf("policy deny comes first: %v", r.body)
	}
	if r := s.HandleExec(ctx, ExecRequest{Target: "mac", Script: "x", Credentials: &[]string{"GITHUB_TOKEN"}}); r.status != 400 {
		t.Fatalf("credentials need a profile: %v", r.body)
	}
	s.modes.Lockdown()
	if r := s.HandleExec(ctx, ExecRequest{Target: "mac", Script: "ls"}); r.status != 403 {
		t.Fatalf("lockdown: %v", r.body)
	}
}

func TestHostGrants(t *testing.T) {
	s, _ := newTest(t)
	s.cfg.Hosts = map[string]*RemoteHost{"mac": {Name: "mac", Label: "Ben's Mac"}}
	if _, msg := s.parseAdminGrant(adminGrantBody{Target: "mac", Session: "*", Minutes: ptr(10.0)}); msg != "" {
		t.Fatalf("mac grant: %s", msg)
	}
	if _, msg := s.parseAdminGrant(adminGrantBody{Target: "printer", Session: "*", Minutes: ptr(10.0)}); !strings.Contains(msg, "configured host (mac)") {
		t.Fatalf("unknown host: %q", msg)
	}
	g := s.grants.Add(api.GrantInput{Target: "mac", Session: "*", Profile: "*", Credentials: api.AnyCreds})
	if g.Target != "mac" {
		t.Fatal("a host grant must stay a host grant")
	}
	if s.grants.Match(GrantRequest{Target: "eldar", Profile: "work", Creds: []string{}}) != nil {
		t.Fatal("a host grant never covers Eldar's VM")
	}
}

func ptr[T any](v T) *T { return &v }

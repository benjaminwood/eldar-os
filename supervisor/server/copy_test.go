package server

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/benjaminwood/eldar-os/supervisor/api"
)

// Command runs the script locally, with $HOME standing in for the VM's.
func (f *fakeRunner) Command(ctx context.Context, user, script string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	cmd.Env = append(os.Environ(), "HOME="+f.home)
	return cmd
}

// newCopyTest: Eldar's VM and a "mac" host as two local home directories.
func newCopyTest(t *testing.T) (s *Supervisor, vm, mac string) {
	t.Helper()
	s, r := newTest(t)
	vm, mac = t.TempDir(), t.TempDir()
	r.home = vm
	key := filepath.Join(t.TempDir(), "mac")
	os.WriteFile(key, []byte("k"), 0o600)
	s.cfg.Hosts = map[string]*RemoteHost{"mac": {Name: "mac", Label: "Ben's Mac", Target: "ben@mac", KeyPath: key}}
	s.cfg.MaxCopyBytes = 1 << 20
	old := hostCommand
	hostCommand = func(ctx context.Context, _ *RemoteHost, script string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, "sh", "-c", script)
		cmd.Env = append(os.Environ(), "HOME="+mac)
		return cmd
	}
	t.Cleanup(func() { hostCommand = old })
	os.MkdirAll(filepath.Join(mac, "Downloads"), 0o755)
	return s, vm, mac
}

func leftovers(t *testing.T, dir string) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(dir, ".supervisor-copy.*"))
	return m
}

func TestCopyToAHostIsTheApprovedFileAndAuditsNoContent(t *testing.T) {
	s, vm, mac := newCopyTest(t)
	os.WriteFile(filepath.Join(vm, "report.csv"), []byte("email,amount\nsecret@example.com,12\n"), 0o600)
	var seen api.Pending
	go func() {
		for i := 0; i < 200; i++ {
			if p := s.approvals.List(); len(p) > 0 {
				seen = p[0]
				s.approvals.Resolve(p[0].ID, api.Approve)
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	r := s.HandleCopy(context.Background(), CopyRequest{From: "eldar:~/report.csv", To: "mac:~/Downloads/", Reason: "for Ben"})
	b := body(t, r)
	dest := filepath.Join(mac, "Downloads", "report.csv")
	if r.status != 200 || b["to"] != "mac:"+dest || b["bytes"] != float64(35) {
		t.Fatalf("copy: %d %v", r.status, b)
	}
	if got, _ := os.ReadFile(dest); string(got) != "email,amount\nsecret@example.com,12\n" {
		t.Fatalf("copied %q", got)
	}
	c := seen.Copy
	if seen.Kind != "copy" || seen.Target != "mac" || c == nil || c.Pull || c.Bytes != 35 || c.SHA256 != b["sha256"] || c.To != "Ben's Mac: "+dest {
		t.Fatalf("the prompt shows the exact file and where it lands: %+v %+v", seen, c)
	}
	audit, _ := os.ReadFile(s.cfg.AuditPath)
	if strings.Contains(string(audit), "secret@example.com") || !strings.Contains(string(audit), b["sha256"].(string)) {
		t.Fatalf("the audit has the hash, never the contents: %s", audit)
	}
	if l := leftovers(t, filepath.Join(mac, "Downloads")); len(l) > 0 {
		t.Fatalf("temp files left: %v", l)
	}
}

func TestCopyRefusesBeforeAsking(t *testing.T) {
	s, vm, mac := newCopyTest(t)
	os.WriteFile(filepath.Join(vm, "a.txt"), []byte("a"), 0o600)
	os.WriteFile(filepath.Join(mac, "Downloads", "a.txt"), []byte("old"), 0o600)
	os.WriteFile(filepath.Join(vm, "big"), make([]byte, 2<<20), 0o600)
	ctx := context.Background()
	msg := func(r response) string { m, _ := body(t, r)["error"].(string); return m }
	for name, c := range map[string]struct {
		req  CopyRequest
		want string
	}{
		"no target":      {CopyRequest{From: "/tmp/a", To: "mac:~/"}, "<target>:<path>"},
		"unknown host":   {CopyRequest{From: "eldar:~/a.txt", To: "nas:~/"}, "configured host (mac)"},
		"relative path":  {CopyRequest{From: "eldar:a.txt", To: "mac:~/"}, "absolute or start with ~/"},
		"same place":     {CopyRequest{From: "eldar:~/a.txt", To: "eldar:/tmp/"}, "Both ends"},
		"missing source": {CopyRequest{From: "eldar:~/nope", To: "mac:~/Downloads/"}, "not a file"},
		"too big":        {CopyRequest{From: "eldar:~/big", To: "mac:~/Downloads/"}, "limited to 1.0 MiB"},
		"exists":         {CopyRequest{From: "eldar:~/a.txt", To: "mac:~/Downloads/"}, "exists (set overwrite"},
		"no such dir":    {CopyRequest{From: "eldar:~/a.txt", To: "mac:~/Nope/"}, "no such directory"},
	} {
		r := s.HandleCopy(ctx, c.req)
		if r.status != 400 || !strings.Contains(msg(r), c.want) {
			t.Errorf("%s: %d %q", name, r.status, msg(r))
		}
	}
	if p := s.approvals.List(); len(p) != 0 {
		t.Fatalf("nothing doomed reaches Ben: %v", p)
	}
	if got, _ := os.ReadFile(filepath.Join(mac, "Downloads", "a.txt")); string(got) != "old" {
		t.Fatal("an existing file is never replaced without overwrite")
	}
	answer(s, api.Approve)
	if r := s.HandleCopy(ctx, CopyRequest{From: "eldar:~/a.txt", To: "mac:~/Downloads/a.txt", Overwrite: true}); r.status != 200 {
		t.Fatalf("overwrite: %v", r.body)
	}
	if got, _ := os.ReadFile(filepath.Join(mac, "Downloads", "a.txt")); string(got) != "a" {
		t.Fatal("overwrite replaces it")
	}
}

func TestCopyKeepsNothingWhenTheFileChangedAfterApproval(t *testing.T) {
	s, vm, mac := newCopyTest(t)
	src := filepath.Join(vm, "r.csv")
	os.WriteFile(src, []byte("approved"), 0o600)
	go func() {
		for i := 0; i < 200; i++ {
			if p := s.approvals.List(); len(p) > 0 {
				os.WriteFile(src, []byte("swapped!"), 0o600) // same size, different bytes
				s.approvals.Resolve(p[0].ID, api.Approve)
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	r := s.HandleCopy(context.Background(), CopyRequest{From: "eldar:" + src, To: "mac:~/Downloads/"})
	if r.status != 422 || !strings.Contains(body(t, r)["error"].(string), "doesn't match the approved file") {
		t.Fatalf("changed file: %d %v", r.status, r.body)
	}
	if _, err := os.Stat(filepath.Join(mac, "Downloads", "r.csv")); err == nil {
		t.Fatal("a file Ben didn't approve was kept")
	}
	if l := leftovers(t, filepath.Join(mac, "Downloads")); len(l) > 0 {
		t.Fatalf("temp files left: %v", l)
	}
}

func TestCopyApprovalRules(t *testing.T) {
	s, vm, mac := newCopyTest(t)
	ctx := context.Background()
	os.WriteFile(filepath.Join(vm, "out.txt"), []byte("out"), 0o600)
	os.WriteFile(filepath.Join(mac, "in.txt"), []byte("in"), 0o600)
	s.grants.Add(api.GrantInput{Target: "mac", Session: "*", Profile: "*", Credentials: api.AnyCreds})
	s.grants.Add(api.GrantInput{Target: "eldar", Session: "*", Profile: "*", Credentials: api.AnyCreds})

	if r := s.HandleCopy(ctx, CopyRequest{From: "eldar:~/out.txt", To: "mac:~/Downloads/"}); r.status != 200 {
		t.Fatalf("a Mac grant covers sending a file to the Mac: %v", r.body)
	}
	// Pulling off the Mac asks even with both grants; with no answer it times out.
	r := s.HandleCopy(ctx, CopyRequest{From: "mac:~/in.txt", To: "eldar:~/"})
	if r.status != 403 || body(t, r)["outcome"] != string(OutUndeliverable) {
		t.Fatalf("a pull always asks: %v", r.body)
	}
	if _, err := os.Stat(filepath.Join(vm, "in.txt")); err == nil {
		t.Fatal("pulled without approval")
	}
	answer(s, api.Deny)
	if r := s.HandleCopy(ctx, CopyRequest{From: "mac:~/in.txt", To: "eldar:~/"}); r.status != 403 || body(t, r)["outcome"] != string(OutDenied) {
		t.Fatalf("denied: %v", r.body)
	}
	answer(s, api.Approve)
	if r := s.HandleCopy(ctx, CopyRequest{From: "mac:~/in.txt", To: "eldar:~/"}); r.status != 200 {
		t.Fatalf("approved pull: %v", r.body)
	}
	s.modes.Lockdown()
	if r := s.HandleCopy(ctx, CopyRequest{From: "eldar:~/out.txt", To: "mac:~/Downloads/again.txt"}); r.status != 403 {
		t.Fatalf("lockdown: %v", r.body)
	}
}

func TestCopyPathsAreNeverShell(t *testing.T) {
	s, vm, mac := newCopyTest(t)
	name := `$(touch pwned) 'x"; y.csv`
	os.WriteFile(filepath.Join(vm, name), []byte("x"), 0o600)
	answer(s, api.Approve)
	if r := s.HandleCopy(context.Background(), CopyRequest{From: "eldar:~/" + name, To: "mac:~/Downloads/"}); r.status != 200 {
		t.Fatalf("odd name: %v", r.body)
	}
	if _, err := os.Stat(filepath.Join(mac, "Downloads", name)); err != nil {
		t.Fatal("copied under its own name")
	}
	for _, d := range []string{vm, mac, "."} {
		if _, err := os.Stat(filepath.Join(d, "pwned")); err == nil {
			t.Fatal("a path ran as a command")
		}
	}
}

package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/benjaminwood/eldar-os/supervisor/api"
)

type fakeRunner struct {
	specs  []ExecSpec
	result ExecResult
	files  map[string]string
}

func (f *fakeRunner) Exec(_ context.Context, s ExecSpec) (ExecResult, error) {
	f.specs = append(f.specs, s)
	if len(s.Argv) == 2 && s.Argv[0] == "cron-status" {
		return ExecResult{Stdout: "running\n"}, nil
	}
	return f.result, nil
}

func (f *fakeRunner) ReadFile(_ context.Context, p string) (string, error) { return f.files[p], nil }

const testPolicy = `
default: ask
rules:
  - match: "allowed *"
    action: allow
  - match: "denied *"
    action: deny
`

// newTest builds a supervisor with profile "work" whose credentials come from
// a pre-filled cache (no 1Password).
func newTest(t *testing.T) (*Supervisor, *fakeRunner) {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "policy.yaml"), []byte(testPolicy), 0o600)
	os.MkdirAll(filepath.Join(dir, "cache"), 0o700)
	os.WriteFile(filepath.Join(dir, "cache", "work.env"), []byte("GITHUB_TOKEN=gh\nOTHER=\"quoted\"\n# comment\n"), 0o600)
	cfg := Config{
		PolicyPath: filepath.Join(dir, "policy.yaml"), AuditPath: filepath.Join(dir, "audit.jsonl"),
		SettingsPath: filepath.Join(dir, "settings.json"), CronsPath: filepath.Join(dir, "crons.json"),
		CacheDir: filepath.Join(dir, "cache"), ApprovalTimeout: 200 * time.Millisecond,
		DefaultCommandTimeout: 300 * time.Second, MaxCommandTimeout: 900 * time.Second,
		MaxOutputBytes: 1 << 20, AgentUser: "eldar",
	}
	profiles := LoadProfiles([]string{"OP_ACCOUNT_ACME_TOKEN=t", "OP_PROFILE_WORK_ACCOUNT=acme", "OP_PROFILE_WORK_ENV=env1"}, "op", time.Second, cfg.CacheDir)
	r := &fakeRunner{result: ExecResult{Stdout: "ok\n"}}
	s, err := New(cfg, profiles, r)
	if err != nil {
		t.Fatal(err)
	}
	return s, r
}

func body(t *testing.T, res response) map[string]any {
	t.Helper()
	b, _ := json.Marshal(res.body)
	var m map[string]any
	json.Unmarshal(b, &m)
	return m
}

func creds(n ...string) *[]string { return &n }

// answer resolves the next prompt with d once it appears.
func answer(s *Supervisor, d api.Decision) {
	go func() {
		for i := 0; i < 100; i++ {
			if p := s.approvals.List(); len(p) > 0 {
				s.approvals.Resolve(p[0].ID, d)
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
}

func TestExecValidation(t *testing.T) {
	s, _ := newTest(t)
	ctx := context.Background()
	for name, req := range map[string]ExecRequest{
		"nothing":         {Profile: "work"},
		"two kinds":       {Command: "ls", File: "/tmp/x", Profile: "work"},
		"no profile":      {Command: "ls"},
		"unknown profile": {Command: "ls", Profile: "nope"},
	} {
		res := s.HandleExec(ctx, req)
		if res.status != 400 || body(t, res)["outcome"] != "bad_request" {
			t.Errorf("%s: %d %v", name, res.status, res.body)
		}
	}
}

func TestExecDecisionOrder(t *testing.T) {
	ctx := context.Background()

	s, r := newTest(t)
	if res := s.HandleExec(ctx, ExecRequest{Command: "allowed", Args: []string{"x"}, Profile: "work", Credentials: creds()}); res.status != 200 {
		t.Fatalf("allow rule: %d %v", res.status, res.body)
	}
	if len(r.specs) != 1 || strings.Join(r.specs[0].Argv, " ") != "allowed x" || len(r.specs[0].Env) != 0 {
		t.Fatalf("ran %+v", r.specs)
	}

	if res := s.HandleExec(ctx, ExecRequest{Command: "denied", Args: []string{"x"}, Profile: "work"}); res.status != 403 || body(t, res)["outcome"] != "blocked_policy" {
		t.Fatalf("deny: %d %v", res.status, res.body)
	}

	// Lockdown beats an allow rule; bypass beats ask but not deny.
	s.modes.Lockdown()
	if res := s.HandleExec(ctx, ExecRequest{Command: "allowed", Args: []string{"x"}, Profile: "work"}); res.status != 403 {
		t.Fatalf("lockdown: %d", res.status)
	}
	s.modes.Bypass(nil)
	if res := s.HandleExec(ctx, ExecRequest{Command: "anything", Profile: "work", Credentials: creds()}); res.status != 200 {
		t.Fatalf("bypass: %d %v", res.status, res.body)
	}
	if res := s.HandleExec(ctx, ExecRequest{Command: "denied", Args: []string{"x"}, Profile: "work"}); res.status != 403 {
		t.Fatalf("bypass must not override deny: %d", res.status)
	}
	s.modes.Revert()

	// A grant covers a verified session's ask.
	s.sessions.Touch("s1", "k", TouchMeta{})
	s.grants.Add(api.GrantInput{Session: "s1", Profile: "work", Credentials: names("GITHUB_TOKEN")})
	if res := s.HandleExec(ctx, ExecRequest{Command: "gh", Profile: "work", Credentials: creds("GITHUB_TOKEN"), SessionID: "s1", SessionKey: "k"}); res.status != 200 {
		t.Fatalf("grant: %d %v", res.status, res.body)
	}
	if res := s.HandleExec(ctx, ExecRequest{Command: "gh", Profile: "work", Credentials: creds("GITHUB_TOKEN"), SessionID: "s1", SessionKey: "wrong"}); res.status != 403 {
		t.Fatalf("wrong key must not use the grant (no one answers, so it should time out): %d", res.status)
	}
}

func TestExecAsksBen(t *testing.T) {
	ctx := context.Background()
	s, r := newTest(t)

	answer(s, api.Approve)
	res := s.HandleExec(ctx, ExecRequest{Command: "deploy", Args: []string{"now"}, Profile: "work", Credentials: creds("GITHUB_TOKEN"), Reason: "ship it"})
	if res.status != 200 || body(t, res)["stdout"] != "ok\n" {
		t.Fatalf("approved: %d %v", res.status, res.body)
	}
	if env := r.specs[len(r.specs)-1].Env; env["GITHUB_TOKEN"] != "gh" || len(env) != 1 {
		t.Fatalf("injected %v, want only GITHUB_TOKEN", env)
	}

	answer(s, api.Deny)
	if res := s.HandleExec(ctx, ExecRequest{Command: "deploy", Profile: "work"}); body(t, res)["outcome"] != "denied" {
		t.Fatalf("denied: %v", res.body)
	}

	// Nobody watching and push off: undeliverable, not a refusal.
	if res := s.HandleExec(ctx, ExecRequest{Command: "deploy", Profile: "work"}); body(t, res)["outcome"] != "undeliverable" {
		t.Fatalf("undeliverable: %v", res.body)
	}
	s.settings.SetMobilePush(true)
	if res := s.HandleExec(ctx, ExecRequest{Command: "deploy", Profile: "work"}); body(t, res)["outcome"] != "timed_out" {
		t.Fatalf("timed out: %v", res.body)
	}

	// Allow always adds "<first word> *".
	answer(s, api.AllowAlways)
	s.HandleExec(ctx, ExecRequest{Command: "make", Args: []string{"build"}, Profile: "work", Credentials: creds()})
	if a, _ := s.policy.Evaluate("make test", ""); a != Allow {
		t.Fatalf("allow always didn't add a rule: %v", a)
	}
}

func TestExecCredentials(t *testing.T) {
	ctx := context.Background()
	s, r := newTest(t)
	s.HandleExec(ctx, ExecRequest{Command: "allowed", Args: []string{"x"}, Profile: "work"}) // whole profile
	if env := r.specs[0].Env; env["GITHUB_TOKEN"] != "gh" || env["OTHER"] != "quoted" {
		t.Fatalf("whole profile: %v", env)
	}
	res := s.HandleExec(ctx, ExecRequest{Command: "allowed", Args: []string{"x"}, Profile: "work", Credentials: creds("MISSING")})
	if res.status != 502 || body(t, res)["outcome"] != "credential_missing" {
		t.Fatalf("missing key: %d %v", res.status, res.body)
	}
	if len(r.specs) != 1 {
		t.Fatal("must not run when a credential is missing")
	}
}

func TestExecFileModeAndArgsJoin(t *testing.T) {
	ctx := context.Background()
	s, r := newTest(t)
	r.files = map[string]string{"/tmp/cmd": "allowed thing | tee out\n"}
	s.HandleExec(ctx, ExecRequest{File: "/tmp/cmd", Profile: "work", Credentials: creds()})
	if got := r.specs[0].Shell; got != "allowed thing | tee out" {
		t.Fatalf("file mode runs verbatim: %q", got)
	}
	// script: the text itself, verbatim (what the MCP server sends).
	script := "allowed 'a b' \"$X\" | tee out"
	s.HandleExec(ctx, ExecRequest{Script: script, Profile: "work", Credentials: creds()})
	if got := r.specs[1].Shell; got != script {
		t.Fatalf("script runs verbatim: %q", got)
	}
	if res := s.HandleExec(ctx, ExecRequest{Script: "x", Command: "y", Profile: "work"}); res.status != 400 {
		t.Fatalf("script and command together: %d", res.status)
	}
	// args present but empty still joins with a space, as before.
	var req ExecRequest
	json.Unmarshal([]byte(`{"command":"allowed","args":[],"profile":"work","credentials":[]}`), &req)
	if res := s.HandleExec(ctx, req); res.status != 200 {
		t.Fatalf("`allowed ` should match `allowed *`: %d %v", res.status, res.body)
	}
}

func TestExecCronAndTimeout(t *testing.T) {
	ctx := context.Background()
	s, r := newTest(t)
	if res := s.HandleExec(ctx, ExecRequest{Command: "x", Profile: "work", CronID: "abcd"}); res.status != 403 {
		t.Fatalf("unlisted cron: %d", res.status)
	}
	s.crons.Add("abcd", "nightly")
	if res := s.HandleExec(ctx, ExecRequest{Command: "x", Profile: "work", CronID: "abcd", Credentials: creds()}); res.status != 200 {
		t.Fatalf("running cron: %d %v", res.status, res.body)
	}
	s.HandleExec(ctx, ExecRequest{Command: "allowed", Args: []string{"x"}, Profile: "work", Credentials: creds(), TimeoutSeconds: 99999})
	if got := r.specs[len(r.specs)-1].Timeout; got != 900*time.Second {
		t.Fatalf("timeout not clamped: %v", got)
	}
}

func TestGrantRequestFlow(t *testing.T) {
	s, _ := newTest(t)
	m := 30.0
	b := GrantRequestBody{Profile: "work", Credentials: json.RawMessage(`["GITHUB_TOKEN"]`), Minutes: &m, SessionID: "s1", SessionKey: "k", Reason: "PRs"}

	// Ben narrows it in the editor.
	go func() {
		for i := 0; i < 100; i++ {
			if p := s.approvals.List(); len(p) > 0 {
				in := *p[0].Grant
				in.Credentials = names()
				if !s.ApproveGrantRequest(p[0].ID, in) {
					t.Error("approve failed")
				}
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	res := s.HandleGrantRequest(b, "")
	if res.status != 200 || body(t, res)["narrowed"] != true {
		t.Fatalf("grant request: %d %v", res.status, res.body)
	}
	if g := s.grants.List(); len(g) != 1 || g[0].Session != "s1" || len(g[0].Credentials.Names) != 0 {
		t.Fatalf("grant: %+v", g)
	}

	// Another key for the same session can't ask.
	if res := s.HandleGrantRequest(GrantRequestBody{Profile: "work", Minutes: &m, SessionID: "s1", SessionKey: "other"}, ""); res.status != 403 {
		t.Fatalf("impostor: %d", res.status)
	}
	bad := 481.0
	if res := s.HandleGrantRequest(GrantRequestBody{Profile: "work", Minutes: &bad, SessionID: "s1", SessionKey: "k"}, ""); res.status != 400 {
		t.Fatalf("minutes range: %d", res.status)
	}
}

func TestRunnerScript(t *testing.T) {
	r := &SSHRunner{}
	got, err := r.Script(ExecSpec{Argv: []string{"echo", "it's"}, Env: map[string]string{"TOKEN": "a'b"}, Cwd: "/home/eldar/my dir", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	want := "umask 002\nexport TOKEN='a'\\''b'\ncd -- '/home/eldar/my dir' || exit 1\nexec timeout -k 5 5 bash -c ''\\''echo'\\'' '\\''it'\\''\\'\\'''\\''s'\\'''\n"
	if got != want {
		t.Fatalf("script:\n%s\nwant:\n%s", got, want)
	}
	if _, err := r.Script(ExecSpec{Argv: []string{"x"}, Env: map[string]string{"BAD-NAME": "v"}}); err == nil {
		t.Fatal("invalid env name must be refused")
	}
}

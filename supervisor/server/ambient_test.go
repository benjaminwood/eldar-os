package server

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

const testAmbient = `
env:
  - dir: ~/workspace
    profile: work
    vars: [GITHUB_TOKEN, SHARED]
  - dir: ~/workspace/acme
    profile: alt
    vars: [SHARED, NOT_THERE]
  - dir: /srv/elsewhere
    profile: nope
    vars: [X]
git:
  Acme: { profile: alt, credential: ACME_PAT }
  gone: { profile: alt, credential: NOT_THERE }
`

// newAmbientTest: newTest's supervisor, plus a second cached profile "alt" and
// the ambient file above.
func newAmbientTest(t *testing.T) *Supervisor {
	t.Helper()
	s, _ := newTest(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(s.cfg.CacheDir, "work.env"), []byte("GITHUB_TOKEN=gh\nSHARED=from-work\n"), 0o600)
	os.WriteFile(filepath.Join(s.cfg.CacheDir, "alt.env"), []byte("SHARED=from-alt\nACME_PAT=pat\n"), 0o600)
	s.profiles = LoadProfiles([]string{"OP_ACCOUNT_ACME_TOKEN=t",
		"OP_PROFILE_WORK_ACCOUNT=acme", "OP_PROFILE_WORK_ENV=env1",
		"OP_PROFILE_ALT_ACCOUNT=acme", "OP_PROFILE_ALT_ENV=env2"}, "op", time.Second, s.cfg.CacheDir)
	os.WriteFile(filepath.Join(dir, "ambient.yaml"), []byte(testAmbient), 0o600)
	s.ambient = NewAmbient(filepath.Join(dir, "ambient.yaml"), "/home/eldar")
	return s
}

func TestAmbientEnv(t *testing.T) {
	s := newAmbientTest(t)
	ctx := context.Background()
	env := func(dir string) (map[string]any, []any) {
		t.Helper()
		res := s.AmbientEnv(ctx, dir)
		if res.status != 200 {
			t.Fatalf("%s: %d %v", dir, res.status, res.body)
		}
		b := body(t, res)
		missing, _ := b["missing"].([]any)
		return b["env"].(map[string]any), missing
	}

	got, missing := env("/home/eldar/workspace/projects/x")
	if !reflect.DeepEqual(got, map[string]any{"GITHUB_TOKEN": "gh", "SHARED": "from-work"}) || len(missing) != 0 {
		t.Errorf("workspace: %v missing %v", got, missing)
	}
	// Deeper rules add to and override shallower ones; absent keys are reported.
	got, missing = env("/home/eldar/workspace/acme/repo")
	if !reflect.DeepEqual(got, map[string]any{"GITHUB_TOKEN": "gh", "SHARED": "from-alt"}) ||
		!reflect.DeepEqual(missing, []any{"alt/NOT_THERE"}) {
		t.Errorf("acme: %v missing %v", got, missing)
	}
	// A sibling that only shares a prefix isn't under the rule.
	if got, _ := env("/home/eldar/workspace2"); len(got) != 0 {
		t.Errorf("workspace2: %v", got)
	}
	// An unknown profile is reported, not fatal.
	if got, missing := env("/srv/elsewhere"); len(got) != 0 || len(missing) != 1 {
		t.Errorf("elsewhere: %v missing %v", got, missing)
	}
	if res := s.AmbientEnv(ctx, "workspace"); res.status != 400 {
		t.Errorf("relative dir: %d", res.status)
	}
}

func TestAmbientGit(t *testing.T) {
	s := newAmbientTest(t)
	ctx := context.Background()
	if res := s.AmbientGit(ctx, "ACME"); res.status != 200 || body(t, res)["token"] != "pat" {
		t.Errorf("acme: %d %v", res.status, res.body)
	}
	if res := s.AmbientGit(ctx, "benjaminwood"); res.status != 404 {
		t.Errorf("no entry: %d %v", res.status, res.body)
	}
	if res := s.AmbientGit(ctx, "gone"); res.status != 502 || body(t, res)["outcome"] != "credential_missing" {
		t.Errorf("missing credential: %d %v", res.status, res.body)
	}
}

func TestAmbientLockdownAndAudit(t *testing.T) {
	s := newAmbientTest(t)
	ctx := context.Background()
	s.AmbientEnv(ctx, "/home/eldar/workspace")
	s.AmbientEnv(ctx, "/home/eldar/workspace")
	s.AmbientGit(ctx, "acme")
	s.AmbientGit(ctx, "acme")
	n := 0
	for _, e := range s.audit.Tail(50) {
		if e.Action == "ambient env" || e.Action == "ambient github token acme" {
			n++
		}
	}
	if n != 2 {
		t.Errorf("audited %d times, want once per fetch kind within the hour", n)
	}

	s.modes.Lockdown()
	if res := s.AmbientEnv(ctx, "/home/eldar/workspace"); res.status != 403 {
		t.Errorf("env in lockdown: %d", res.status)
	}
	if res := s.AmbientGit(ctx, "acme"); res.status != 403 {
		t.Errorf("git in lockdown: %d", res.status)
	}
}

func TestAmbientNoFile(t *testing.T) {
	s := newAmbientTest(t)
	s.ambient = NewAmbient(filepath.Join(t.TempDir(), "absent.yaml"), "/home/eldar")
	if res := s.AmbientEnv(context.Background(), "/home/eldar/workspace"); res.status != 200 || len(body(t, res)["env"].(map[string]any)) != 0 {
		t.Errorf("no file: %d %v", res.status, res.body)
	}
}

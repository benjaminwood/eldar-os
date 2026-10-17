package server

import (
	"os"
	"path/filepath"
	"testing"
)

func writePolicy(t *testing.T, yaml string) *Policy {
	t.Helper()
	p := filepath.Join(t.TempDir(), "policy.yaml")
	if err := os.WriteFile(p, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	return NewPolicy(p)
}

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pattern, s string
		want       bool
	}{
		{"git *", "git status", true},
		{"git *", "git", false},
		{"git *", "git ", true},
		{"git status", "git status", true},
		{"git status", "git status --short", false},
		{"*", "anything at all", true},
		{"aws s3 ls*", "aws s3 ls s3://bucket", true},
		{"rm -rf /tmp/x.y", "rm -rf /tmp/xzy", false}, // dots are literal
		{"echo (hi)", "echo (hi)", true},               // parens are literal
		{"a+b", "a+b", true},
		{"x*", "x\nrm -rf /", false}, // * doesn't cross newlines
		{"colou?r", "color", true},   // as in the original, ? is a regex quantifier
	}
	for _, c := range cases {
		if got := globMatch(c.pattern, c.s); got != c.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", c.pattern, c.s, got, c.want)
		}
	}
}

func TestPolicyOrderAndDefault(t *testing.T) {
	p := writePolicy(t, `
default: ask
rules:
  - match: "git push *"
    action: deny
  - match: "git *"
    action: allow
  - match: "rm *"
    action: deny
`)
	for cmd, want := range map[string]Action{
		"git push origin main": Deny,
		"git status":           Allow,
		"rm -rf x":             Deny,
		"ls":                   Ask,
	} {
		if got, err := p.Evaluate(cmd, ""); err != nil || got != want {
			t.Errorf("Evaluate(%q) = %v, %v; want %v", cmd, got, err, want)
		}
	}
}

func TestPolicyAws(t *testing.T) {
	p := writePolicy(t, `
default: ask
rules:
  - match: "aws s3 ls*"
    action: allow
    aws: readonly
  - match: "kubectl get *"
    action: allow
    aws: ["dev", "staging"]
  - match: "echo *"
    action: allow
  - match: "aws iam *"
    action: deny
`)
	cases := []struct {
		cmd, aws string
		want     Action
	}{
		{"aws s3 ls", "readonly", Allow},
		{"aws s3 ls", "admin", Ask},       // allow rule doesn't name admin
		{"aws s3 ls", "", Allow},          // rule with aws still covers non-AWS requests
		{"kubectl get pods", "staging", Allow},
		{"kubectl get pods", "prod", Ask},
		{"echo hi", "", Allow},
		{"echo hi", "dev", Ask},             // rules without aws never auto-allow AWS
		{"aws iam list-users", "dev", Deny}, // deny applies regardless
	}
	for _, c := range cases {
		if got, _ := p.Evaluate(c.cmd, c.aws); got != c.want {
			t.Errorf("Evaluate(%q, aws=%q) = %v, want %v", c.cmd, c.aws, got, c.want)
		}
	}
}

func TestPolicyRuntimeRulesFirstAndLiveReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.yaml")
	os.WriteFile(path, []byte("default: ask\nrules: []\n"), 0o600)
	p := NewPolicy(path)
	if got, _ := p.Evaluate("make build", ""); got != Ask {
		t.Fatalf("got %v", got)
	}
	p.AddAllowRule("make *", "")
	if got, _ := p.Evaluate("make build", ""); got != Allow {
		t.Fatalf("runtime rule: got %v", got)
	}
	p.AddAllowRule("aws *", "dev")
	if got, _ := p.Evaluate("aws s3 ls", "prod"); got != Ask {
		t.Fatalf("allow-always for one AWS profile widened: got %v", got)
	}
	os.WriteFile(path, []byte("default: deny\nrules: []\n"), 0o600)
	if got, _ := p.Evaluate("ls", ""); got != Deny {
		t.Fatalf("policy file not re-read: got %v", got)
	}
}

func TestPolicyUnreadableIsAnError(t *testing.T) {
	p := NewPolicy(filepath.Join(t.TempDir(), "missing.yaml"))
	if _, err := p.Evaluate("ls", ""); err == nil {
		t.Fatal("expected an error for a missing policy file")
	}
}

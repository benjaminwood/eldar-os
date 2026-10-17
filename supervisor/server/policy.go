package server

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// Action is what policy says about a command.
type Action string

const (
	Allow Action = "allow"
	Ask   Action = "ask"
	Deny  Action = "deny"
)

// awsField is a rule's `aws`: absent, one profile, or a list ("*" = any).
type awsField struct {
	set   bool
	names []string
}

func (a *awsField) UnmarshalYAML(n *yaml.Node) error {
	a.set = true
	if n.Kind == yaml.ScalarNode {
		a.names = []string{n.Value}
		return nil
	}
	return n.Decode(&a.names)
}

type rule struct {
	Match  string   `yaml:"match"`
	Action Action   `yaml:"action"`
	Aws    awsField `yaml:"aws"`
}

type policyFile struct {
	Default Action `yaml:"default"`
	Rules   []rule `yaml:"rules"`
}

// Policy evaluates commands against the policy file, re-read on every call so
// edits apply immediately, plus "allow always" rules added at runtime (in
// memory only).
type Policy struct {
	path    string
	mu      sync.Mutex
	runtime []rule
}

func NewPolicy(path string) *Policy { return &Policy{path: path} }

func (p *Policy) load() (policyFile, error) {
	var f policyFile
	b, err := os.ReadFile(p.path)
	if err != nil {
		return f, err
	}
	if err := yaml.Unmarshal(b, &f); err != nil {
		return f, fmt.Errorf("policy %s: %w", p.path, err)
	}
	return f, nil
}

// Evaluate returns the first matching rule's action: runtime rules first, then
// the file's, else the file's default. awsProfile is the AWS profile the
// request mints credentials for, if any: an allow rule only covers that when
// its `aws` names the profile (or "*"); deny and ask rules apply regardless.
func (p *Policy) Evaluate(command, awsProfile string) (Action, error) {
	f, err := p.load()
	if err != nil {
		return "", err
	}
	p.mu.Lock()
	rules := append(append([]rule{}, p.runtime...), f.Rules...)
	p.mu.Unlock()
	for _, r := range rules {
		if !globMatch(r.Match, command) {
			continue
		}
		if r.Action == Allow && !coversAws(r, awsProfile) {
			continue
		}
		return r.Action, nil
	}
	return f.Default, nil
}

func coversAws(r rule, awsProfile string) bool {
	if !r.Aws.set {
		return awsProfile == ""
	}
	if awsProfile == "" {
		return true
	}
	for _, n := range r.Aws.names {
		if n == "*" || n == awsProfile {
			return true
		}
	}
	return false
}

// AddAllowRule adds an in-memory allow rule ahead of the file's rules. With
// awsProfile, the rule covers that AWS profile alone.
func (p *Policy) AddAllowRule(pattern, awsProfile string) {
	r := rule{Match: pattern, Action: Allow}
	if awsProfile != "" {
		r.Aws = awsField{set: true, names: []string{awsProfile}}
	}
	p.mu.Lock()
	p.runtime = append([]rule{r}, p.runtime...)
	p.mu.Unlock()
}

// globMatch: `*` matches anything (not newlines); the whole command must match.
// Escapes the same characters as the original implementation, so patterns
// written for it behave identically.
var globSpecial = regexp.MustCompile(`[.+^${}()|\[\]\\]`)

func globMatch(pattern, s string) bool {
	escaped := globSpecial.ReplaceAllStringFunc(pattern, func(m string) string { return `\` + m })
	re, err := regexp.Compile("^" + strings.ReplaceAll(escaped, "*", ".*") + "$")
	if err != nil {
		return false
	}
	return re.MatchString(s)
}

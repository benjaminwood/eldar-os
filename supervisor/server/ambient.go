package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/benjaminwood/eldar-os/supervisor/api"
	"gopkg.in/yaml.v3"
)

// Ambient credentials: what agents in Eldar's VM get without asking
// (docs/decisions/0015-ambient-credentials.md). A private file names them,
// environment variables by directory and GitHub tokens by repo owner, and the
// values come from the profiles' 1Password Environments like any other
// credential:
//
//	env:
//	  - dir: ~/workspace                 # this directory and everything under it
//	    profile: personal
//	    vars: [OPENAI_API_KEY]
//	  - dir: ~/workspace/github/acme     # deeper rules add to and override shallower ones
//	    profile: acme
//	    vars: [RWX_ACCESS_TOKEN]
//	git:
//	  acme: { profile: acme, credential: GITHUB_PAT_ACME }
//
// `~` is the agent user's home. The file is re-read on every request, so edits
// apply immediately.
//
// Directory scoping organises; it doesn't protect. The VM names the directory
// it asks for, so anything in there can get any directory's set. Whether a
// credential is listed at all, and its own scope, are the boundary.
type ambientEnvRule struct {
	Dir     string   `yaml:"dir"`
	Profile string   `yaml:"profile"`
	Vars    []string `yaml:"vars"`
}

type ambientGitRule struct {
	Profile    string `yaml:"profile"`
	Credential string `yaml:"credential"`
}

type ambientFile struct {
	Env []ambientEnvRule          `yaml:"env"`
	Git map[string]ambientGitRule `yaml:"git"`
}

type Ambient struct {
	path, home string

	mu     sync.Mutex
	logged map[string]time.Time // audit key -> last logged
}

// auditEvery: the same ambient fetch (a directory's set, an owner's token) is
// audited at most this often. git and direnv ask constantly.
const auditEvery = time.Hour

func NewAmbient(path, home string) *Ambient {
	return &Ambient{path: path, home: home, logged: map[string]time.Time{}}
}

// load reads the file; none configured, or none there yet, means nothing is
// ambient.
func (a *Ambient) load() (ambientFile, error) {
	var f ambientFile
	if a.path == "" {
		return f, nil
	}
	b, err := os.ReadFile(a.path)
	if os.IsNotExist(err) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if err := yaml.Unmarshal(b, &f); err != nil {
		return f, fmt.Errorf("%s: %w", a.path, err)
	}
	return f, nil
}

func (a *Ambient) expand(dir string) string {
	if dir == "~" || strings.HasPrefix(dir, "~/") {
		dir = a.home + dir[1:]
	}
	return filepath.Clean(dir)
}

func under(dir, root string) bool {
	return root == "/" || dir == root || strings.HasPrefix(dir, root+"/")
}

// envRules are the rules covering dir, shallowest first so deeper ones win.
func (a *Ambient) envRules(f ambientFile, dir string) []ambientEnvRule {
	var out []ambientEnvRule
	for _, r := range f.Env {
		r.Dir = a.expand(r.Dir)
		if under(dir, r.Dir) {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return len(out[i].Dir) < len(out[j].Dir) })
	return out
}

// due reports whether key should be audited now, and marks it if so.
func (a *Ambient) due(key string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if t, ok := a.logged[key]; ok && time.Since(t) < auditEvery {
		return false
	}
	a.logged[key] = time.Now()
	return true
}

func (s *Supervisor) ambientLocked() (response, bool) {
	if mode, _ := s.modes.Status(); mode == api.ModeLockdown {
		return response{403, map[string]any{"error": "Lockdown: no credentials are handed out until Ben lifts it.", "outcome": OutBlockedPolicy}}, true
	}
	return response{}, false
}

// AmbientEnv: the environment variables for dir (absolute, in the VM).
// Variables a profile doesn't have are reported in `missing` rather than
// failing the rest, so one absent key doesn't empty a whole directory.
func (s *Supervisor) AmbientEnv(ctx context.Context, dir string) response {
	if res, locked := s.ambientLocked(); locked {
		return res
	}
	if !filepath.IsAbs(dir) {
		return response{400, map[string]any{"error": "'dir' must be an absolute path.", "outcome": OutBadRequest}}
	}
	dir = filepath.Clean(dir)
	f, err := s.ambient.load()
	if err != nil {
		return response{500, map[string]any{"error": err.Error(), "outcome": OutInternal}}
	}
	env, missing := map[string]string{}, []string{}
	read := map[string]map[string]string{} // profile -> its Environment
	for _, r := range s.ambient.envRules(f, dir) {
		p, ok := s.profiles.Get(r.Profile)
		if !ok {
			missing = append(missing, fmt.Sprintf("%s (unknown profile '%s')", strings.Join(r.Vars, ", "), r.Profile))
			continue
		}
		if read[p.Name] == nil {
			all, err := s.profiles.Read(ctx, p)
			if err != nil {
				return response{502, map[string]any{"error": err.Error(), "outcome": OutCredentialMissing}}
			}
			read[p.Name] = all
		}
		for _, k := range r.Vars {
			if v, ok := read[p.Name][k]; ok && envName.MatchString(k) {
				env[k] = v
			} else {
				missing = append(missing, p.Name+"/"+k)
			}
		}
	}
	names := sortedKeys(env)
	if len(names) > 0 && s.ambient.due("env "+dir+" "+strings.Join(names, ",")) {
		credJSON, _ := json.Marshal(names)
		s.audit.Log(AuditEntry{RequestID: s.nextID(), Action: "ambient env", Decision: "allow",
			DecisionReason: "Ambient for " + dir + " (ambient.yaml): " + strings.Join(names, ", "),
			Mode: string(api.ModeNormal), Cwd: dir, Credentials: credJSON})
	}
	return response{200, map[string]any{"dir": dir, "env": env, "missing": missing}}
}

// AmbientGit: the GitHub token for repos owned by owner, if one is ambient.
// 404 otherwise, and git falls back to Eldar's App (github.go).
func (s *Supervisor) AmbientGit(ctx context.Context, owner string) response {
	if res, locked := s.ambientLocked(); locked {
		return res
	}
	owner = strings.ToLower(strings.TrimSpace(owner))
	f, err := s.ambient.load()
	if err != nil {
		return response{500, map[string]any{"error": err.Error(), "outcome": OutInternal}}
	}
	var rule ambientGitRule
	found := false
	for k, r := range f.Git {
		if strings.ToLower(k) == owner {
			rule, found = r, true
		}
	}
	if owner == "" || !found {
		return response{404, map[string]any{"error": fmt.Sprintf("No ambient GitHub token for '%s'.", owner)}}
	}
	p, ok := s.profiles.Get(rule.Profile)
	if !ok {
		return response{500, map[string]any{"error": fmt.Sprintf("ambient.yaml: unknown profile '%s' for '%s'.", rule.Profile, owner), "outcome": OutInternal}}
	}
	got, err := s.profiles.Credentials(ctx, p, []string{rule.Credential})
	if err != nil {
		return response{502, map[string]any{"error": err.Error(), "outcome": OutCredentialMissing}}
	}
	if s.ambient.due("git " + owner) {
		credJSON, _ := json.Marshal([]string{rule.Credential})
		s.audit.Log(AuditEntry{RequestID: s.nextID(), Action: "ambient github token " + owner, Decision: "allow",
			DecisionReason: "Ambient GitHub token for " + owner + " (ambient.yaml)", Profile: p.Name,
			Mode: string(api.ModeNormal), Credentials: credJSON})
	}
	return response{200, map[string]any{"owner": owner, "token": got[rule.Credential]}}
}

func (s *Supervisor) ambientRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /ambient/env", func(w http.ResponseWriter, r *http.Request) {
		res := s.AmbientEnv(r.Context(), r.URL.Query().Get("dir"))
		sendJSON(w, res.status, res.body)
	})
	mux.HandleFunc("GET /ambient/git", func(w http.ResponseWriter, r *http.Request) {
		res := s.AmbientGit(r.Context(), r.URL.Query().Get("owner"))
		sendJSON(w, res.status, res.body)
	})
}

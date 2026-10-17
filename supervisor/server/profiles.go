package server

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/benjaminwood/eldar-os/supervisor/api"
)

// Credentials live in 1Password Environments, one per profile. A profile is a
// (account, Environment) pair; each account contributes a service-account
// token. Configuration comes from the environment:
//
//	OP_ACCOUNT_<NAME>_TOKEN     service-account token for an account
//	OP_PROFILE_<NAME>_ACCOUNT   which account a profile uses
//	OP_PROFILE_<NAME>_ENV       the profile's 1Password Environment ID
//
// Names are case-insensitive and hyphens equal underscores, so `my-team` in a
// value matches OP_ACCOUNT_MY_TEAM_TOKEN.
type Profile struct {
	Name, Account, Token, EnvID string
}

type Profiles struct {
	profiles map[string]Profile
	Warnings []string
	opBin    string
	timeout  time.Duration
	cacheDir string
}

func canon(s string) string { return strings.ReplaceAll(strings.ToLower(s), "-", "_") }

var (
	reAccount = regexp.MustCompile(`^OP_ACCOUNT_(.+)_TOKEN$`)
	reProfAcc = regexp.MustCompile(`^OP_PROFILE_(.+)_ACCOUNT$`)
	reProfEnv = regexp.MustCompile(`^OP_PROFILE_(.+)_ENV$`)
)

// LoadProfiles builds the registry from KEY=VALUE pairs (os.Environ()).
func LoadProfiles(environ []string, opBin string, timeout time.Duration, cacheDir string) *Profiles {
	tokens := map[string]string{}
	type raw struct{ account, env string }
	raws := map[string]*raw{}
	get := func(n string) *raw {
		if raws[n] == nil {
			raws[n] = &raw{}
		}
		return raws[n]
	}
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || v == "" {
			continue
		}
		if m := reAccount.FindStringSubmatch(k); m != nil {
			tokens[canon(m[1])] = v
		} else if m := reProfAcc.FindStringSubmatch(k); m != nil {
			get(canon(m[1])).account = canon(v)
		} else if m := reProfEnv.FindStringSubmatch(k); m != nil {
			get(canon(m[1])).env = v
		}
	}
	p := &Profiles{profiles: map[string]Profile{}, opBin: opBin, timeout: timeout, cacheDir: cacheDir}
	for name, r := range raws {
		up := strings.ToUpper(name)
		switch {
		case r.account == "":
			p.Warnings = append(p.Warnings, fmt.Sprintf("profile '%s': missing OP_PROFILE_%s_ACCOUNT — excluded", name, up))
		case r.env == "":
			p.Warnings = append(p.Warnings, fmt.Sprintf("profile '%s': missing OP_PROFILE_%s_ENV — excluded", name, up))
		case tokens[r.account] == "":
			p.Warnings = append(p.Warnings, fmt.Sprintf("profile '%s': references account '%s' but OP_ACCOUNT_%s_TOKEN is not set — excluded", name, r.account, strings.ToUpper(r.account)))
		default:
			p.profiles[name] = Profile{Name: name, Account: r.account, Token: tokens[r.account], EnvID: r.env}
		}
	}
	sort.Strings(p.Warnings)
	return p
}

func (p *Profiles) Names() []string {
	out := make([]string, 0, len(p.profiles))
	for n := range p.profiles {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func (p *Profiles) Get(name string) (Profile, bool) {
	pr, ok := p.profiles[canon(name)]
	return pr, ok
}

// CredError is a credential problem; Key is the missing name, if that's what
// it is.
type CredError struct {
	Key string
	Msg string
}

func (e *CredError) Error() string { return e.Msg }

func (p *Profiles) cacheFile(pr Profile) string { return filepath.Join(p.cacheDir, pr.Name+".env") }

// Read returns a profile's whole Environment. Read-through cache: the first
// read fetches from 1Password and writes cacheDir/<profile>.env (0600); later
// reads skip 1Password until the cache is cleared (from the admin UIs).
func (p *Profiles) Read(ctx context.Context, pr Profile) (map[string]string, error) {
	if b, err := os.ReadFile(p.cacheFile(pr)); err == nil {
		return parseDotenv(string(b)), nil
	}
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, p.opBin, "environment", "read", pr.EnvID)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"OP_SERVICE_ACCOUNT_TOKEN=" + pr.Token,
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 500 {
			msg = msg[:500]
		}
		return nil, &CredError{Msg: fmt.Sprintf("op environment read failed for profile '%s': %v %s", pr.Name, err, msg)}
	}
	if err := os.MkdirAll(p.cacheDir, 0o700); err == nil {
		tmp := fmt.Sprintf("%s.tmp.%d", p.cacheFile(pr), os.Getpid())
		if os.WriteFile(tmp, out, 0o600) == nil {
			os.Rename(tmp, p.cacheFile(pr))
		} else {
			os.Remove(tmp)
		}
	}
	return parseDotenv(string(out)), nil
}

// Credentials resolves what a command gets injected. An explicit list
// (including an empty one) is honoured literally; nil means the whole
// profile. Asking for nothing skips 1Password entirely.
func (p *Profiles) Credentials(ctx context.Context, pr Profile, requested []string) (map[string]string, error) {
	explicit := requested != nil
	if explicit && len(requested) == 0 {
		return map[string]string{}, nil
	}
	env, err := p.Read(ctx, pr)
	if err != nil {
		if explicit {
			return nil, err
		}
		logf("op: environment read failed for profile '%s' (whole-profile path): %v", pr.Name, err)
		return map[string]string{}, nil
	}
	if !explicit {
		return env, nil
	}
	out := map[string]string{}
	var missing []string
	for _, k := range requested {
		if v, ok := env[k]; ok {
			out[k] = v
		} else {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return nil, &CredError{Key: missing[0], Msg: fmt.Sprintf("keys missing from profile '%s' Environment: %s", pr.Name, strings.Join(missing, ", "))}
	}
	return out, nil
}

// Cache lists cached credential sets (names and times, never contents).
func (p *Profiles) Cache() []api.CacheEntry {
	ents, err := os.ReadDir(p.cacheDir)
	out := []api.CacheEntry{}
	if err != nil {
		return out
	}
	for _, e := range ents {
		name, ok := strings.CutSuffix(e.Name(), ".env")
		if !ok || !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		kind := "1password"
		if strings.HasPrefix(name, "aws-") {
			kind = "aws"
		}
		out = append(out, api.CacheEntry{Name: name, Kind: kind, CachedAt: info.ModTime().UnixMilli()})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// ClearCache removes one cached set by name, or all when name is empty.
func (p *Profiles) ClearCache(name string) []string {
	removed := []string{}
	for _, e := range p.Cache() {
		if name != "" && e.Name != name {
			continue
		}
		if os.Remove(filepath.Join(p.cacheDir, e.Name+".env")) == nil {
			removed = append(removed, e.Name)
		}
	}
	return removed
}

// parseDotenv is tolerant: skips blanks and comments, strips one pair of
// matching quotes.
func parseDotenv(s string) map[string]string {
	out := map[string]string{}
	for _, raw := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		line := strings.TrimLeft(raw, " \t")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.Index(line, "=")
		if eq <= 0 {
			continue
		}
		k, v := strings.TrimSpace(line[:eq]), line[eq+1:]
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		out[k] = v
	}
	return out
}

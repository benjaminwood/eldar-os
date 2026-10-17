package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/benjaminwood/eldar-os/supervisor/api"
)

// Remote hosts: machines other than Eldar's VM that agents may run commands
// on, through the supervisor (request target = the host's name). Ben's Mac
// ("mac") is the first. Each host is configured privately (hosts.json in the
// secrets repo: SSH target, pinned host key, login shell, a description for
// agents) with its own private key, readable only by the supervisor.
//
// Approval state is per host: global bypass and eldar grants never cover a
// host. Only that host's grants and allow list skip the prompt; lockdown and
// policy denies (on the command, or on "<host>: <command>") still apply.
//
// Execution: ssh to the host, running its login shell with a script on stdin.
// The command goes base64-encoded and is decoded there, so what runs is
// byte-for-byte what Ben approved. Credentials are exported inside that
// script, never on a command line. A watchdog kills the command's whole
// process tree at the timeout. The script is POSIX-shell (bash or zsh).

var validHostName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)

// RemoteHost is one configured host.
type RemoteHost struct {
	Name        string `json:"name"`        // request target, e.g. "mac"
	Label       string `json:"label"`       // for Ben, e.g. "Ben's Mac"
	Description string `json:"description"` // for agents: what it is, what to use it for
	Target      string `json:"target"`      // user@host for ssh
	Shell       string `json:"shell"`       // e.g. "/bin/zsh -l -s"; default "bash -l -s"
	KeyPath     string `json:"-"`
	KnownHosts  string `json:"-"`

	mu       sync.Mutex
	lastTest *api.MacTest
}

// hostsFile is hosts.json: the hosts and each one's pinned host key (a
// known_hosts line without the host name, e.g. "ssh-ed25519 AAAA…").
type hostsFile []struct {
	Name, Label, Description, Target, Shell string
	HostKey                                 string `json:"hostKey"`
}

// LoadHosts reads hosts.json; each host's key is keyDir/<name> (missing key =
// the host is listed but not usable) and its pinned host key is written to
// stateDir/known_hosts-<name>.
func LoadHosts(path, keyDir, stateDir string) (map[string]*RemoteHost, error) {
	hosts := map[string]*RemoteHost{}
	if path == "" {
		return hosts, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return hosts, nil
	} else if err != nil {
		return nil, err
	}
	var f hostsFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, h := range f {
		if !validHostName.MatchString(h.Name) || h.Name == "eldar" || h.Name == "browser" {
			return nil, fmt.Errorf("%s: bad host name %q", path, h.Name)
		}
		if h.Target == "" || h.HostKey == "" {
			return nil, fmt.Errorf("%s: host %s needs target and hostKey", path, h.Name)
		}
		_, addr, _ := strings.Cut(h.Target, "@")
		kh := fmt.Sprintf("%s/known_hosts-%s", stateDir, h.Name)
		if err := os.WriteFile(kh, []byte(addr+" "+strings.TrimSpace(h.HostKey)+"\n"), 0o600); err != nil {
			return nil, err
		}
		rh := &RemoteHost{Name: h.Name, Label: h.Label, Description: h.Description, Target: h.Target, Shell: h.Shell,
			KeyPath: keyDir + "/" + h.Name, KnownHosts: kh}
		if rh.Label == "" {
			rh.Label = h.Name
		}
		if rh.Shell == "" {
			rh.Shell = "bash -l -s"
		}
		hosts[h.Name] = rh
	}
	return hosts, nil
}

func (h *RemoteHost) Configured() bool {
	if h == nil || h.Target == "" {
		return false
	}
	_, err := os.Stat(h.KeyPath)
	return err == nil
}

func (h *RemoteHost) Addr() string {
	if h == nil {
		return ""
	}
	_, a, _ := strings.Cut(h.Target, "@")
	return a
}

// hostScript is what runs on the host (exported for tests).
func hostScript(command, cwd string, env map[string]string, timeout time.Duration) string {
	var b strings.Builder
	if cwd != "" {
		if rest, ok := strings.CutPrefix(cwd, "~/"); ok {
			fmt.Fprintf(&b, "cd -- \"$HOME\"/%s || exit 97\n", shellQuote(rest))
		} else {
			fmt.Fprintf(&b, "cd -- %s || exit 97\n", shellQuote(cwd))
		}
	} else {
		b.WriteString("cd ~\n")
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		if envName.MatchString(k) {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "export %s=%s\n", k, shellQuote(env[k]))
	}
	// Without a terminal there's no job control, so the watchdog kills the
	// command's tree by walking it with pgrep rather than by process group.
	fmt.Fprintf(&b, `__tree() { for __c in $(pgrep -P $2 2>/dev/null); do __tree $1 $__c; done; kill -$1 $2 2>/dev/null; }
__cmd=$(printf %%s %s | base64 -d)
( eval "$__cmd" ) </dev/null &
__pid=$!
( sleep %d; __tree TERM $__pid; sleep 5; __tree KILL $__pid ) </dev/null >/dev/null 2>&1 &
__dog=$!
wait $__pid; __rc=$?
__tree TERM $__dog
exit $__rc
`, shellQuote(base64.StdEncoding.EncodeToString([]byte(command))), max(1, int(timeout/time.Second)))
	return b.String()
}

func (h *RemoteHost) Run(ctx context.Context, command, cwd string, env map[string]string, timeout time.Duration, maxOutput int) (ExecResult, error) {
	if !h.Configured() {
		return ExecResult{}, fmt.Errorf("host %s isn't configured on this supervisor", h.Name)
	}
	// Backstop in case the connection hangs past the host-side watchdog.
	ctx, cancel := context.WithTimeout(ctx, timeout+25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh",
		"-i", h.KeyPath, "-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10",
		"-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile="+h.KnownHosts,
		"-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=4", "-o", "LogLevel=ERROR",
		h.Target, h.Shell)
	cmd.Stdin = strings.NewReader(hostScript(command, cwd, env, timeout))
	out, errw := &capped{limit: maxOutput}, &capped{limit: maxOutput}
	cmd.Stdout, cmd.Stderr = out, errw
	started := time.Now()
	err := cmd.Run()
	res := ExecResult{Stdout: out.buf.String(), Stderr: errw.buf.String(), StdoutBytes: out.total, StderrBytes: errw.total,
		Truncated: out.truncated() || errw.truncated()}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		res.ExitCode = ee.ExitCode()
		if res.ExitCode == -1 {
			res.ExitCode, res.TimedOut = 137, true
		}
	default:
		return res, err
	}
	if (res.ExitCode == 143 || res.ExitCode == 137) && time.Since(started) >= timeout {
		res.TimedOut = true
	}
	if res.ExitCode == 255 && res.Stderr == "" {
		res.Stderr = "ssh: connection to " + h.Label + " failed"
	}
	return res, nil
}

// Test checks the connection with a harmless command.
func (h *RemoteHost) Test(ctx context.Context) api.MacTest {
	t := api.MacTest{At: time.Now().UnixMilli()}
	r, err := h.Run(ctx, `printf '%s · %s' "$(scutil --get ComputerName 2>/dev/null || hostname)" "$(if command -v sw_vers >/dev/null; then echo "macOS $(sw_vers -productVersion)"; else uname -sr; fi)"`, "", nil, 20*time.Second, 4096)
	switch {
	case err != nil:
		t.Detail = err.Error()
	case r.ExitCode == 0:
		t.OK, t.Detail = true, strings.TrimSpace(r.Stdout)
	default:
		d := strings.TrimSpace(r.Stderr + r.Stdout)
		if d == "" {
			d = fmt.Sprintf("exit %d", r.ExitCode)
		}
		t.Detail = truncRunes(d, 300)
	}
	h.mu.Lock()
	h.lastTest = &t
	h.mu.Unlock()
	return t
}

func (h *RemoteHost) LastTest() *api.MacTest {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lastTest
}

// HostAllow is Ben's "always allow" lists, one per host: globs over the whole
// command (`*` matches anything), persisted in host-allow.json.
type HostAllow struct {
	path  string
	mu    sync.Mutex
	lists map[string][]string
}

func NewHostAllow(path string) *HostAllow {
	a := &HostAllow{path: path, lists: map[string][]string{}}
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, &a.lists)
	}
	return a
}

func (a *HostAllow) List(host string) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string{}, a.lists[host]...)
}

func (a *HostAllow) saveLocked() {
	b, _ := json.MarshalIndent(a.lists, "", "  ")
	tmp := a.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err == nil {
		os.Rename(tmp, a.path)
	} else {
		logf("host allow lists: %v", err)
	}
}

// Add a pattern. One that matches everything is refused: that's a grant.
func (a *HostAllow) Add(host, p string) error {
	p = strings.TrimSpace(p)
	if p == "" || len(p) > 300 {
		return errors.New("pattern must be 1-300 characters")
	}
	if strings.Trim(p, "* ") == "" {
		return errors.New("a pattern that matches everything isn't allowed; use a grant")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !slices.Contains(a.lists[host], p) {
		a.lists[host] = append(a.lists[host], p)
		a.saveLocked()
	}
	return nil
}

func (a *HostAllow) Remove(host, p string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := len(a.lists[host])
	a.lists[host] = slices.DeleteFunc(a.lists[host], func(x string) bool { return x == p })
	if len(a.lists[host]) == n {
		return false
	}
	a.saveLocked()
	return true
}

func (a *HostAllow) Match(host, command string) string {
	for _, p := range a.List(host) {
		re := "(?s)^" + strings.ReplaceAll(regexp.QuoteMeta(p), `\*`, ".*") + "$"
		if ok, _ := regexp.MatchString(re, command); ok {
			return p
		}
	}
	return ""
}

func (s *Supervisor) host(name string) *RemoteHost { return s.cfg.Hosts[name] }

func (s *Supervisor) isHost(name string) bool { _, ok := s.cfg.Hosts[name]; return ok }

// hostNames: the configured hosts, sorted.
func (s *Supervisor) hostNames() []string {
	names := make([]string, 0, len(s.cfg.Hosts))
	for n := range s.cfg.Hosts {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

func (s *Supervisor) hostStatuses() []api.HostStatus {
	out := []api.HostStatus{}
	for _, n := range s.hostNames() {
		h := s.host(n)
		out = append(out, api.HostStatus{Name: n, Label: h.Label, Description: h.Description, Configured: h.Configured(),
			Host: h.Addr(), LastTest: h.LastTest(), Allow: s.hostAllow.List(n)})
	}
	return out
}

// macStatus is the "mac" host in the shape the UIs already show.
func (s *Supervisor) macStatus() api.MacStatus {
	h := s.host("mac")
	return api.MacStatus{Configured: h.Configured(), Host: h.Addr(), LastTest: h.LastTest(), Allow: s.hostAllow.List("mac")}
}

// handleHostExec: POST /exec with a host as the target.
func (s *Supervisor) handleHostExec(ctx context.Context, id int, mode api.Mode, req ExecRequest) response {
	bad := func(msg string) response { return fail(400, id, OutBadRequest, msg) }
	h := s.host(req.Target)
	if h == nil {
		hint := "No other hosts are configured."
		if names := s.hostNames(); len(names) > 0 {
			hint = "Configured hosts: " + strings.Join(names, ", ") + "."
		}
		return bad(fmt.Sprintf("Unknown target %q. %s", req.Target, hint))
	}
	if !h.Configured() {
		return bad(fmt.Sprintf("%s isn't reachable from this supervisor (no key).", h.Label))
	}
	if len(req.MCP) > 0 || req.File != "" {
		return bad("Commands on other hosts take 'script' or 'command'.")
	}
	command := req.Script
	switch {
	case command != "" && req.Command != "":
		return bad("Give one of 'script' or 'command'.")
	case command == "" && req.Args != nil:
		command = req.Command + " " + strings.Join(req.Args, " ")
	case command == "":
		command = req.Command
	}
	if strings.TrimSpace(command) == "" {
		return bad("Empty command.")
	}
	// Credentials are optional; asking for any needs a profile.
	var profile Profile
	if req.Profile != "" && req.Profile != "*" {
		p, ok := s.profiles.Get(req.Profile)
		if !ok {
			return bad(fmt.Sprintf("Unknown profile '%s'. %s", req.Profile, s.profilesHint()))
		}
		profile = p
	} else if len(req.credList()) > 0 {
		return bad("Credentials need a 'profile'.")
	}
	if req.AwsProfile != "" {
		return bad("AWS credentials aren't available for other hosts.")
	}
	creds := req.credList()
	if profile.Name == "" {
		creds = []string{}
	} else if req.Credentials == nil {
		creds = nil // whole profile
	}
	timeout := s.cfg.DefaultCommandTimeout
	if req.TimeoutSeconds > 0 {
		timeout = min(time.Duration(req.TimeoutSeconds)*time.Second, s.cfg.MaxCommandTimeout)
	}
	action := h.Name + ": " + command
	verified := s.sessions.Touch(req.SessionID, req.SessionKey, TouchMeta{Cwd: req.Cwd, HerdrPane: req.Context.herdrPane(), Action: action})
	base := req.auditMeta(AuditEntry{RequestID: id, Action: action, Reason: req.Reason, Profile: profile.Name, Mode: string(mode)})
	deny := func(decision, why string, out Outcome, msg string) response {
		e := base
		e.Decision, e.DecisionReason = decision, why
		s.audit.Log(e)
		return fail(403, id, out, msg)
	}
	if mode == api.ModeLockdown {
		return deny("lockdown", "Supervisor is in lockdown mode", OutBlockedPolicy, "Supervisor is in lockdown mode")
	}
	for _, c := range []string{command, action} {
		if a, err := s.policy.Evaluate(c, ""); err != nil {
			return fail(500, id, OutInternal, "Policy unreadable: "+err.Error())
		} else if a == Deny {
			return deny("deny", "Denied by policy rule", OutBlockedPolicy, "Action denied by policy")
		}
	}

	why := ""
	if g := s.grants.Match(GrantRequest{Target: h.Name, SessionID: req.SessionID, Verified: verified, Profile: profile.Name, Creds: creds}); g != nil {
		why = fmt.Sprintf("%s grant #%d", h.Label, g.ID)
		if g.Session != "*" {
			why += fmt.Sprintf(" (session %.8s)", g.Session)
		}
	} else if p := s.hostAllow.Match(h.Name, command); p != "" {
		why = h.Label + " allow list: " + p
	}
	if why == "" {
		pattern := strings.Fields(command)[0] + " *"
		var cp *[]string
		if creds != nil {
			cp = &creds
		}
		cwd := req.Cwd
		if cwd == "" {
			cwd = "~"
		}
		result := s.awaitHuman(id, api.Pending{
			Kind: "exec", Target: h.Name, Host: h.Label, Title: truncRunes(strings.SplitN(command, "\n", 2)[0], 200),
			Command: command, Reason: req.Reason, Profile: profile.Name, Credentials: cp,
			TimeoutSeconds: int(timeout / time.Second), SessionID: req.SessionID, SessionVerified: verified, Cwd: cwd,
			AllowAlways: pattern,
		}, func() { s.hostAllow.Add(h.Name, pattern) })
		switch result {
		case "approved":
			why = "Approved by Ben"
		case "allow_always":
			why = "Approved by Ben + " + h.Label + " allow list: " + pattern
		case "denied":
			return deny("denied", "Denied by Ben", OutDenied, "Request denied by Ben. Do NOT retry or attempt to work around this.")
		case "undeliverable":
			return deny("timeout", "Approval prompt could not be delivered to any channel", OutUndeliverable,
				"Approval could not be delivered on any channel — nobody saw this request and nothing was executed. This is NOT a refusal; it is safe to retry once.")
		default:
			return deny("timeout", "No response within timeout", OutTimedOut,
				"Request timed out — Ben did not approve within the timeout window. Do NOT retry. Inform Ben that the request was not approved.")
		}
	}

	var env map[string]string
	if profile.Name != "" {
		c, err := s.profiles.Credentials(ctx, profile, creds)
		if err != nil {
			var ce *CredError
			out := OutInternal
			if errors.As(err, &ce) && ce.Key != "" {
				out = OutCredentialMissing
			}
			e := base
			e.Decision, e.DecisionReason = "deny", "Credential fetch failed: "+err.Error()
			s.audit.Log(e)
			return fail(502, id, out, "Credential fetch from 1Password failed. The command was NOT executed. Inform Ben.", "detail", err.Error())
		}
		env = c
	}
	started := time.Now()
	res, err := h.Run(ctx, command, req.Cwd, env, timeout, s.cfg.MaxOutputBytes)
	dur := time.Since(started).Milliseconds()
	e := base
	e.Decision, e.DecisionReason, e.DurationMs = "allow", why, &dur
	if err != nil {
		e.Result = &AuditResult{ExitCode: -1, Stderr: err.Error()}
		s.audit.Log(e)
		return fail(500, id, OutInternal, "Execution on "+h.Label+" failed", "detail", err.Error())
	}
	e.TimedOut, e.Truncated = res.TimedOut, res.Truncated
	e.Result = &AuditResult{ExitCode: res.ExitCode, Stdout: truncBytes(res.Stdout, 1000), Stderr: truncBytes(res.Stderr, 1000)}
	s.audit.Log(e)
	body := map[string]any{"requestId": id, "exitCode": res.ExitCode, "stdout": res.Stdout, "stderr": res.Stderr, "host": h.Name}
	if res.TimedOut {
		body["timedOut"], body["timeoutSeconds"] = true, int(timeout/time.Second)
	}
	if res.Truncated {
		body["truncated"], body["stdoutBytes"], body["stderrBytes"], body["maxOutputBytes"] = true, res.StdoutBytes, res.StderrBytes, s.cfg.MaxOutputBytes
	}
	status := 200
	if res.ExitCode != 0 {
		status = 422
	}
	return response{status, body}
}

// agentHosts: what agents see in /status, for supervisor_host_exec.
func (s *Supervisor) agentHosts() []map[string]string {
	out := []map[string]string{}
	for _, n := range s.hostNames() {
		if h := s.host(n); h.Configured() {
			out = append(out, map[string]string{"name": n, "label": h.Label, "description": h.Description})
		}
	}
	return out
}

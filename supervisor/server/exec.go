package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/benjaminwood/eldar-os/supervisor/api"
)

// Outcome classifies a failed request for the client: the right reaction
// differs (never retry a denial; an undeliverable prompt is safe to retry once).
type Outcome string

const (
	OutDenied            Outcome = "denied"
	OutTimedOut          Outcome = "timed_out"
	OutUndeliverable     Outcome = "undeliverable"
	OutBlockedPolicy     Outcome = "blocked_policy"
	OutCredentialMissing Outcome = "credential_missing"
	OutMintFailed        Outcome = "mint_failed"
	OutBadRequest        Outcome = "bad_request"
	OutInternal          Outcome = "internal"
)

// ExecRequest is the body of POST /exec.
type ExecRequest struct {
	Command        string          `json:"command,omitempty"`
	Script         string          `json:"script,omitempty"` // run verbatim under bash
	Args           []string        `json:"args"`             // present but empty ([]) still joins with a space, as before
	File           string          `json:"file,omitempty"`
	MCP            json.RawMessage `json:"mcp,omitempty"`
	Reason         string          `json:"reason,omitempty"`
	CronID         string          `json:"cronId,omitempty"`
	Profile        string          `json:"profile,omitempty"`
	Credentials    *[]string       `json:"credentials,omitempty"` // nil = whole profile
	AwsProfile     string          `json:"awsProfile,omitempty"`
	Cwd            string          `json:"cwd,omitempty"`
	TimeoutSeconds float64         `json:"timeoutSeconds,omitempty"`
	SessionID      string          `json:"sessionId,omitempty"`
	SessionKey     string          `json:"sessionKey,omitempty"`
	Target         string          `json:"target,omitempty"`
	Context        *RequestContext `json:"context,omitempty"`
}

// RequestContext carries hints about where the caller runs.
type RequestContext struct {
	Herdr *struct {
		Pane   string `json:"pane"`
		Socket string `json:"socket"`
	} `json:"herdr,omitempty"`
}

func (c *RequestContext) herdrPane() string {
	if c == nil || c.Herdr == nil {
		return ""
	}
	return c.Herdr.Pane
}

type response struct {
	status int
	body   any
}

func fail(status int, id int, out Outcome, msg string, extra ...any) response {
	b := map[string]any{"error": msg, "outcome": out, "requestId": id}
	for i := 0; i+1 < len(extra); i += 2 {
		b[extra[i].(string)] = extra[i+1]
	}
	return response{status, b}
}

func (r *ExecRequest) credList() []string {
	if r.Credentials == nil {
		return nil
	}
	if *r.Credentials == nil {
		return []string{}
	}
	return *r.Credentials
}

// auditMeta is the request context recorded with every audit entry.
func (r *ExecRequest) auditMeta(e AuditEntry) AuditEntry {
	if r.Credentials != nil {
		e.Credentials, _ = json.Marshal(r.credList())
	} else if len(r.MCP) == 0 {
		e.Credentials = json.RawMessage("null")
	}
	e.AwsProfile, e.Cwd, e.SessionID = r.AwsProfile, r.Cwd, r.SessionID
	e.Target = "eldar"
	if r.Target != "" {
		e.Target = r.Target
	}
	return e
}

func (s *Supervisor) profilesHint() string {
	if names := s.profiles.Names(); len(names) > 0 {
		return "Available profiles: " + strings.Join(names, ", ") + "."
	}
	return "No profiles are configured on this supervisor."
}

// HandleExec runs POST /exec: validate, decide (cron, lockdown, policy,
// bypass, grants, Ben), then execute.
func (s *Supervisor) HandleExec(ctx context.Context, req ExecRequest) response {
	id := s.nextID()
	mode := s.modes.Current()

	if req.Target != "" && req.Target != "eldar" {
		return s.handleHostExec(ctx, id, mode, req)
	}
	timeout := s.cfg.DefaultCommandTimeout
	if req.TimeoutSeconds > 0 {
		timeout = min(time.Duration(req.TimeoutSeconds)*time.Second, s.cfg.MaxCommandTimeout)
	}

	n := 0
	for _, set := range []bool{len(req.MCP) > 0, req.File != "", req.Command != "", req.Script != ""} {
		if set {
			n++
		}
	}
	if n == 0 {
		return fail(400, id, OutBadRequest, "Missing 'script', 'command', 'file', or 'mcp' field")
	}
	if n > 1 {
		return fail(400, id, OutBadRequest, "'script', 'command', 'file', and 'mcp' are mutually exclusive")
	}
	if len(req.MCP) > 0 {
		return fail(400, id, OutBadRequest, "MCP calls aren't available on this supervisor yet.")
	}
	if req.Profile == "" {
		return fail(400, id, OutBadRequest, "Missing required field 'profile'. Specify which 1Password credential profile to use. "+s.profilesHint())
	}
	profile, ok := s.profiles.Get(req.Profile)
	if !ok {
		return fail(400, id, OutBadRequest, fmt.Sprintf("Unknown profile '%s'. %s", req.Profile, s.profilesHint()))
	}
	if req.AwsProfile != "" && !s.aws.Has(req.AwsProfile) {
		return fail(400, id, OutBadRequest, fmt.Sprintf("Unknown AWS profile '%s'. List them with supervisor_aws_profiles.", req.AwsProfile))
	}

	var full, shell string
	if req.Script != "" {
		full, shell = req.Script, req.Script
	} else if req.File != "" {
		content, err := s.runner.ReadFile(ctx, req.File)
		if err != nil {
			return fail(400, id, OutBadRequest, "Failed to read command file: "+err.Error())
		}
		full = strings.TrimSpace(content)
		shell = full
	} else if req.Args != nil {
		full = req.Command + " " + strings.Join(req.Args, " ")
	} else {
		full = req.Command
	}

	verified := s.sessions.Touch(req.SessionID, req.SessionKey, TouchMeta{Cwd: req.Cwd, HerdrPane: req.Context.herdrPane(), Action: full})
	base := req.auditMeta(AuditEntry{RequestID: id, Action: full, Reason: req.Reason, Profile: profile.Name, Mode: string(mode)})
	run := func(why string) response { return s.execute(ctx, id, full, shell, req, profile, base, why, timeout) }

	if req.CronID != "" {
		ok, why := s.verifyCron(ctx, req.CronID)
		if ok {
			return run(fmt.Sprintf("Cron auto-approved (%s)", req.CronID))
		}
		e := base
		e.Decision, e.DecisionReason = "deny", why
		s.audit.Log(e)
		return fail(403, id, OutBlockedPolicy, why)
	}
	if mode == api.ModeLockdown {
		e := base
		e.Decision, e.DecisionReason = "lockdown", "Supervisor is in lockdown mode"
		s.audit.Log(e)
		return fail(403, id, OutBlockedPolicy, "Supervisor is in lockdown mode")
	}
	action, err := s.policy.Evaluate(full, req.AwsProfile)
	if err != nil {
		return fail(500, id, OutInternal, "Policy unreadable: "+err.Error())
	}
	if action == Deny {
		e := base
		e.Decision, e.DecisionReason = "deny", "Denied by policy rule"
		s.audit.Log(e)
		return fail(403, id, OutBlockedPolicy, "Action denied by policy")
	}
	if mode == api.ModeBypass {
		return run("Bypass mode active")
	}
	if action == Allow {
		return run("Allowed by policy rule")
	}
	if g := s.grants.Match(GrantRequest{Target: "eldar", SessionID: req.SessionID, Verified: verified, Profile: profile.Name, Creds: req.credList(), AwsProfile: req.AwsProfile}); g != nil {
		label := fmt.Sprintf("Bypass grant #%d", g.ID)
		if g.Session != "*" {
			label += fmt.Sprintf(" (session %.8s)", g.Session)
		}
		return run(label)
	}

	title := strings.SplitN(full, "\n", 2)[0]
	var creds *[]string
	if req.Credentials != nil {
		c := req.credList()
		creds = &c
	}
	result := s.awaitHuman(id, api.Pending{
		Kind: "exec", Title: truncRunes(title, 200), Command: full, Reason: req.Reason,
		Profile: profile.Name, Credentials: creds, AwsProfile: req.AwsProfile,
		TimeoutSeconds: int(timeout / time.Second), SessionID: req.SessionID,
		SessionVerified: verified, Cwd: req.Cwd,
	}, func() { s.policy.AddAllowRule(strings.SplitN(full, " ", 2)[0]+" *", req.AwsProfile) })

	switch result {
	case "approved":
		return run("Approved by Ben")
	case "allow_always":
		return run("Approved by Ben + added to allowlist")
	}
	e := base
	e.Decision = "timeout"
	switch result {
	case "denied":
		e.Decision, e.DecisionReason = "denied", "Denied by Ben"
	case "undeliverable":
		e.DecisionReason = "Approval prompt could not be delivered to any channel"
	default:
		e.DecisionReason = "No response within timeout"
	}
	s.audit.Log(e)
	switch result {
	case "undeliverable":
		return fail(403, id, OutUndeliverable, "Approval could not be delivered on any channel — nobody saw this request and nothing was executed. This is NOT a refusal; it is safe to retry once.")
	case "denied":
		return fail(403, id, OutDenied, "Request denied by Ben. Do NOT retry or attempt to work around this.")
	default:
		return fail(403, id, OutTimedOut, "Request timed out — Ben did not approve within the timeout window. Do NOT retry. Inform Ben that the request was not approved and you will not continue with this action.")
	}
}

func truncRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// execute fetches credentials and runs the command, auditing the result.
func (s *Supervisor) execute(ctx context.Context, id int, full, shell string, req ExecRequest, profile Profile, base AuditEntry, why string, timeout time.Duration) response {
	base.DecisionReason = why
	creds, err := s.profiles.Credentials(ctx, profile, req.credList())
	if err != nil {
		var ce *CredError
		key := ""
		if errors.As(err, &ce) {
			key = ce.Key
		}
		e := base
		e.Decision = "deny"
		e.DecisionReason = "Credential fetch failed"
		if key != "" {
			e.DecisionReason += " for " + key
		}
		e.DecisionReason += ": " + err.Error()
		s.audit.Log(e)
		out, forKey := OutInternal, ""
		if key != "" {
			out, forKey = OutCredentialMissing, " for "+key
		}
		return fail(502, id, out, "Credential fetch from 1Password failed"+forKey+". The command was NOT executed. Inform Ben.", "detail", err.Error())
	}

	if req.AwsProfile != "" {
		env, err := s.aws.Creds(ctx, req.AwsProfile)
		if err != nil {
			e := base
			e.Decision, e.DecisionReason = "deny", err.Error()
			s.audit.Log(e)
			return fail(502, id, OutMintFailed, fmt.Sprintf("AWS credentials for '%s' couldn't be minted, so nothing ran: %v. "+
				"This isn't a refusal: Ben needs to sign the supervisor in to AWS again (`supervisor aws-login` on eldar-os; one sign-in covers every role).", req.AwsProfile, err))
		}
		if creds == nil {
			creds = map[string]string{}
		}
		for k, v := range env {
			creds[k] = v
		}
	}
	spec := ExecSpec{Env: creds, Cwd: req.Cwd, Timeout: timeout, MaxOutput: s.cfg.MaxOutputBytes}
	if shell != "" {
		spec.Shell = shell
	} else {
		spec.Argv = append([]string{req.Command}, req.Args...)
	}
	started := time.Now()
	res, err := s.runner.Exec(ctx, spec)
	if err != nil {
		e := base
		e.Decision = "allow"
		e.Result = &AuditResult{ExitCode: -1, Stderr: err.Error()}
		s.audit.Log(e)
		return fail(500, id, OutInternal, "Execution failed", "detail", err.Error())
	}
	dur := time.Since(started).Milliseconds()
	e := base
	e.Decision = "allow"
	e.DurationMs = &dur
	e.TimedOut, e.Truncated = res.TimedOut, res.Truncated
	e.Result = &AuditResult{ExitCode: res.ExitCode, Stdout: truncBytes(res.Stdout, 1000), Stderr: truncBytes(res.Stderr, 1000)}
	s.audit.Log(e)

	body := map[string]any{"requestId": id, "exitCode": res.ExitCode, "stdout": res.Stdout, "stderr": res.Stderr}
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

func truncBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}

// verifyCron: the cron must be on the allow list and currently running (per
// `cron-status` in the agent's environment, run as the agent).
func (s *Supervisor) verifyCron(ctx context.Context, id string) (bool, string) {
	if !s.crons.Has(id) {
		return false, fmt.Sprintf("Cron %s is not in the authorized list", id)
	}
	res, err := s.runner.Exec(ctx, ExecSpec{Argv: []string{"cron-status", id}, User: s.cfg.AgentUser, Timeout: 30 * time.Second, MaxOutput: 4096})
	if err != nil {
		return false, fmt.Sprintf("Cron %s status check failed: %v", id, err)
	}
	if st := strings.TrimSpace(res.Stdout); st != "running" {
		return false, fmt.Sprintf("Cron %s status is %q, not \"running\"", id, st)
	}
	return true, ""
}

// awaitHuman registers the prompt with the admin API and waits for Ben.
// Returns approved | denied | allow_always | timeout | undeliverable.
func (s *Supervisor) awaitHuman(id int, info api.Pending, allowAlways func()) string {
	now := time.Now()
	info.ID, info.Channel = id, "none"
	info.CreatedAt, info.ExpiresAt = now.UnixMilli(), now.Add(s.cfg.ApprovalTimeout).UnixMilli()
	ch := s.approvals.Add(info)
	what := info.Title
	if info.Kind == "exec" && info.Target != "" && info.Target != "eldar" {
		what = "on " + info.Host + ": " + what
	} else if info.Kind == "grant_request" {
		what = "grant: " + what
	}
	s.nudgeHerdr(id, info.SessionID, what)
	select {
	case d := <-ch:
		if d == api.AllowAlways {
			if allowAlways == nil {
				return "approved"
			}
			allowAlways()
		}
		return string(d)
	case <-time.After(s.cfg.ApprovalTimeout):
		s.approvals.Remove(id)
		if s.n.Subscribers() > 0 || s.settings.Get().MobilePush {
			return "timeout"
		}
		return "undeliverable"
	}
}

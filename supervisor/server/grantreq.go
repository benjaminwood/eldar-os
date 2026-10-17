package server

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/benjaminwood/eldar-os/supervisor/api"
)

// A session asks Ben for a grant covering its own future requests: one target
// with the same scope a grant Ben makes has. Ben approves it as asked, narrows
// it in the grant editor first, or denies it. Approval creates an ordinary
// grant for that session. The session must present its pinned key, since
// session-specific grants only match verified requests.

// GrantRequestBody is POST /grant-request.
type GrantRequestBody struct {
	Target      string          `json:"target,omitempty"`
	Profile     string          `json:"profile,omitempty"`
	Credentials json.RawMessage `json:"credentials,omitempty"`
	Aws         json.RawMessage `json:"aws,omitempty"`
	Arc         json.RawMessage `json:"arc,omitempty"`
	Minutes     *float64        `json:"minutes,omitempty"`
	Reason      string          `json:"reason,omitempty"`
	SessionID   string          `json:"sessionId,omitempty"`
	SessionKey  string          `json:"sessionKey,omitempty"`
	Cwd         string          `json:"cwd,omitempty"`
	Context     *RequestContext `json:"context,omitempty"`

	// Older clients' /scoped-bypass-request.
	AwsProfile string `json:"awsProfile,omitempty"`
}

// parseScope reads a scope field: absent → def; "*" → any; a list of
// non-empty names. ok=false when malformed.
func parseScope(raw json.RawMessage, def api.Creds) (api.Creds, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return def, true
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return api.AnyCreds, s == "*"
	}
	var names []string
	if json.Unmarshal(raw, &names) != nil {
		return api.Creds{}, false
	}
	for _, n := range names {
		if n == "" {
			return api.Creds{}, false
		}
	}
	return normCreds(api.Creds{Names: names}), true
}

func listOf(c api.Creds, anyText, none string) string {
	if c.Any {
		return anyText
	}
	if len(c.Names) == 0 {
		return none
	}
	return strings.Join(c.Names, ", ")
}

func (s *Supervisor) describeScope(g api.GrantInput) string {
	where := "Eldar"
	if h := s.host(g.Target); h != nil {
		where = h.Label
	}
	profile := g.Profile
	if profile == "*" {
		profile = "ANY"
	}
	scope := fmt.Sprintf("%s: profile %s · creds %s", where, profile, listOf(g.Credentials, "ANY", "none"))
	if g.Target == "eldar" || g.Target == "" {
		scope += " · AWS " + listOf(g.Aws, "ANY role", "none")
	}
	return scope
}

func coversEverything(g api.GrantInput) bool {
	wide := g.Profile == "*" && g.Credentials.Any
	if g.Target != "eldar" && g.Target != "" {
		return wide
	}
	return wide && g.Aws.Any
}

// grantScope validates a request's scope into a GrantInput, or says what's
// wrong.
func (s *Supervisor) grantScope(b GrantRequestBody, minutes int) (api.GrantInput, string) {
	target := b.Target
	if target == "" {
		target = "eldar"
	}
	host := s.isHost(target)
	switch {
	case target == "eldar", host:
	case target == "browser":
		return api.GrantInput{}, "Arc grants aren't available on this supervisor yet"
	default:
		return api.GrantInput{}, "target must be eldar, browser or a configured host (" + strings.Join(s.hostNames(), ", ") + ")"
	}
	profile := b.Profile
	if profile == "" && host {
		profile = "*"
	}
	if profile == "" {
		return api.GrantInput{}, "eldar grants need a profile. Available: " + strings.Join(s.profiles.Names(), ", ")
	}
	if profile != "*" {
		if _, ok := s.profiles.Get(profile); !ok {
			return api.GrantInput{}, fmt.Sprintf("unknown profile '%s'. Available: %s", profile, strings.Join(s.profiles.Names(), ", "))
		}
	}
	creds, ok := parseScope(b.Credentials, api.Creds{Names: []string{}})
	if !ok {
		return api.GrantInput{}, `credentials must be a list of names, [] for none, or "*"`
	}
	aws, ok := parseScope(b.Aws, api.Creds{Names: []string{}})
	if !ok {
		return api.GrantInput{}, `aws must be a list of AWS profiles, [] for none, or "*"`
	}
	if host && (aws.Any || len(aws.Names) > 0) {
		return api.GrantInput{}, "commands on other hosts can't use AWS; leave aws out"
	}
	if len(aws.Names) > 0 {
		return api.GrantInput{}, "unknown AWS profiles: " + strings.Join(aws.Names, ", ") + " (AWS isn't set up on this supervisor yet)"
	}
	if target != "eldar" {
		aws = api.Creds{Names: []string{}}
	}
	note := "requested"
	if b.Reason != "" {
		note += ": " + b.Reason
	}
	return api.GrantInput{Target: target, Session: b.SessionID, Profile: profile, Credentials: creds, Aws: aws, Minutes: &minutes, Note: truncRunes(note, 200)}, ""
}

// HandleGrantRequest runs POST /grant-request (and the older bypass routes,
// with legacy naming the action for the session list).
func (s *Supervisor) HandleGrantRequest(b GrantRequestBody, legacy string) response {
	id := s.nextID()
	bad := func(status int, msg string) response {
		out := OutBlockedPolicy
		if status == 400 {
			out = OutBadRequest
		}
		return fail(status, id, out, msg)
	}
	if b.Minutes == nil || *b.Minutes != float64(int(*b.Minutes)) || *b.Minutes <= 0 || *b.Minutes > 480 {
		return bad(400, "'minutes' must be a whole number between 1 and 480.")
	}
	minutes := int(*b.Minutes)
	mode := s.modes.Current()
	if mode == api.ModeLockdown {
		return bad(403, "The supervisor is in lockdown; no grants can be made.")
	}
	if b.SessionID == "" || b.SessionKey == "" {
		return bad(400, "A grant request needs this session's ID and key (its supervisor client sends both).")
	}
	action := legacy
	if action == "" {
		action = "grant request"
	}
	if !s.sessions.Touch(b.SessionID, b.SessionKey, TouchMeta{Cwd: b.Cwd, HerdrPane: b.Context.herdrPane(), Action: action}) {
		return bad(403, "This session's key doesn't match the one the supervisor pinned, so a grant for it would never apply. Ask Ben to re-pin the session.")
	}
	proposal, why := s.grantScope(b, minutes)
	if why != "" {
		return bad(400, why)
	}
	scope := s.describeScope(proposal)
	title := fmt.Sprintf("Grant for %dm · %s", minutes, scope)
	if coversEverything(proposal) {
		title = "⚠ EVERYTHING — " + title
	}
	var creds *[]string
	if !proposal.Credentials.Any {
		c := proposal.Credentials.Names
		creds = &c
	}
	result := s.awaitHuman(id, api.Pending{
		Kind: "grant_request", Title: title, Reason: b.Reason, Profile: proposal.Profile,
		Credentials: creds, TimeoutSeconds: minutes * 60, SessionID: b.SessionID,
		SessionVerified: true, Cwd: b.Cwd, Grant: &proposal,
	}, nil)

	override, narrowed := s.takeOverride(id)
	if result == "approved" || result == "allow_always" {
		input := proposal
		if narrowed {
			input = override
		}
		g := s.grants.Add(input)
		granted := s.describeScope(input)
		why := "Approved by Ben as requested"
		if narrowed {
			why = "Approved by Ben, narrowed in the grant editor"
		}
		s.audit.Log(AuditEntry{RequestID: id, Action: fmt.Sprintf("GRANT #%d (requested) %s", g.ID, granted), Reason: b.Reason, Decision: "approved", DecisionReason: why, Mode: string(mode), SessionID: b.SessionID})
		msg := fmt.Sprintf("Grant #%d for this session", g.ID)
		if narrowed {
			msg += " (Ben changed the scope)"
		}
		return response{200, map[string]any{
			"requestId": id, "grantId": g.ID, "scopeId": g.ID, "expiresAt": g.ExpiresAt, "grant": g,
			"narrowed": narrowed, "message": msg + ": " + granted + ".",
		}}
	}
	decision, why, out, msg := "timeout", "No response within timeout", OutTimedOut, "Ben didn't answer the grant request."
	switch result {
	case "denied":
		decision, why, out, msg = "denied", "Denied by Ben", OutDenied, "Ben denied the grant request. Don't retry or work around it."
	case "undeliverable":
		why, out, msg = "Approval prompt could not be delivered", OutUndeliverable, "The grant request couldn't be delivered; safe to retry once."
	}
	s.audit.Log(AuditEntry{RequestID: id, Action: "GRANT REQUEST " + scope, Reason: b.Reason, Decision: decision, DecisionReason: why, Mode: string(mode), SessionID: b.SessionID})
	return fail(403, id, out, msg)
}

func (s *Supervisor) takeOverride(id int) (api.GrantInput, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.grantOverrides[id]
	delete(s.grantOverrides, id)
	return g, ok
}

// ApproveGrantRequest approves a pending grant request with the scope Ben
// chose in the editor.
func (s *Supervisor) ApproveGrantRequest(id int, in api.GrantInput) bool {
	p, ok := s.approvals.Get(id)
	if !ok || p.Kind != "grant_request" || p.Grant == nil {
		return false
	}
	if in.Session == "" {
		in.Session = p.Grant.Session
	}
	s.mu.Lock()
	s.grantOverrides[id] = in
	s.mu.Unlock()
	if !s.approvals.Resolve(id, api.Approve) {
		s.takeOverride(id)
		return false
	}
	return true
}

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

type tool struct {
	Name        string
	Description string
	Schema      func(profiles []string, def string) map[string]any
	run         func(ctx context.Context, args map[string]any) map[string]any
}

func textResult(s string) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": s}}}
}

func errorText(s string) map[string]any {
	r := textResult(s)
	r["isError"] = true
	return r
}

// The supervisor returns a machine-readable outcome with each failure. The
// distinctions matter because the right reaction differs: treating a failed
// notification as a refusal halts work nobody objected to, and treating a
// refusal as a glitch retries something Ben declined.
var guidance = map[string]string{
	"denied": "Ben declined this. Stop. Do not retry and do not work around it.",
	"timed_out": "The approval window closed with no answer: Ben didn't decline, he was away. Don't retry " +
		"silently; say there's no answer yet and let him decide when to re-issue.",
	"undeliverable": "The request never reached Ben: a transport failure, not a refusal. Safe to retry " +
		"once. If it fails the same way, tell Ben his approval channels are down.",
	"blocked_policy": "A policy rule rejected this before anyone saw it. Don't retry, and don't reshape the " +
		"command to slip past the rule; tell Ben which rule blocked it.",
	"credential_missing": "The profile has no credential by that name, so nothing ran. This is a caller bug " +
		"(a typo or a stale doc), not an outage: check the real names with supervisor_credentials.",
	"mint_failed": "Minting AWS credentials failed, so nothing ran, and this isn't a refusal: Ben's AWS " +
		"sign-in on the supervisor has lapsed. Don't retry or work around it; ask him to sign in again.",
	"bad_request": "The call itself was malformed (missing or unknown profile, no command, or something " +
		"this supervisor doesn't do yet). Fix the call; this isn't something to ask Ben about.",
	"internal": "Unrecognised supervisor error. Treat it as a stop: don't retry or work around it without asking Ben.",
}

func failure(data map[string]any) map[string]any {
	outcome, _ := data["outcome"].(string)
	g, ok := guidance[outcome]
	if !ok {
		outcome, g = "internal", guidance["internal"]
	}
	msg := fmt.Sprint(data["error"])
	if d, ok := data["detail"].(string); ok && d != "" {
		msg += "\n" + d
	}
	return errorText(fmt.Sprintf("SUPERVISOR REQUEST %s\n\n%s\n\n%s", strings.ToUpper(outcome), msg, g))
}

func unreachable(err error) map[string]any {
	if _, ok := err.(errNoResponse); ok {
		return errorText("SUPERVISOR REQUEST TIMED_OUT\n\n" + err.Error() + "\n\nNo verdict came back: the request may " +
			"still be in front of Ben, or the command may still be running. Don't silently retry; check " +
			"supervisor_audit to see whether it ran.")
	}
	return errorText("SUPERVISOR UNREACHABLE\n\n" + err.Error() + "\n\nThe supervisor couldn't be contacted, so " +
		"nothing ran and nobody declined anything. Check it with supervisor_status.")
}

func str(args map[string]any, k string) string {
	s, _ := args[k].(string)
	return s
}

func strList(v any) ([]string, bool) {
	list, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := []string{}
	for _, x := range list {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out, true
}

func cwd(args map[string]any) string {
	if c := str(args, "cwd"); c != "" {
		return c
	}
	if c := os.Getenv("CLAUDE_PROJECT_DIR"); c != "" {
		return c
	}
	if c, err := os.Getwd(); err == nil {
		return c
	}
	return os.Getenv("HOME")
}

// profiles asks the supervisor which 1Password profiles it has, and its
// default, so tool schemas name the real ones (they're configuration, not code).
func (s *server) profiles(ctx context.Context) ([]string, string) {
	data, err := s.c.get(ctx, "/status")
	if err != nil {
		return nil, ""
	}
	op, _ := data["op"].(map[string]any)
	var names []string
	ps, _ := op["profiles"].([]any)
	for _, p := range ps {
		if m, ok := p.(map[string]any); ok {
			if n, ok := m["name"].(string); ok {
				names = append(names, n)
			}
		}
	}
	sort.Strings(names)
	def, _ := op["defaultProfile"].(string)
	return names, def
}

func profileSchema(profiles []string, def, desc string) map[string]any {
	p := map[string]any{"type": "string", "description": desc}
	if len(profiles) > 0 {
		p["enum"] = profiles
	}
	if def != "" {
		p["default"] = def
	}
	return p
}

func (s *server) describeTools(ctx context.Context) []map[string]any {
	profiles, def := s.profiles(ctx)
	var out []map[string]any
	for _, t := range s.toolList() {
		out = append(out, map[string]any{"name": t.Name, "description": t.Description, "inputSchema": t.Schema(profiles, def)})
	}
	return out
}

func object(props map[string]any, required ...string) map[string]any {
	o := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		o["required"] = required
	}
	return o
}

func (s *server) toolList() []tool {
	return []tool{
		{
			Name: "supervisor_exec",
			Description: "Run a command with credentials from 1Password, injected for that one run. You never hold " +
				"the credentials. The supervisor checks the command against policy; most need Ben's approval " +
				"(he answers in the supervisor's TUI, web UI or on his phone).\n\n" +
				"The command is plain text and runs verbatim under bash, as `ben` in this VM: write nested " +
				"quotes, $VARS, heredocs and newlines exactly as they should run. There's no quoting layer.\n\n" +
				"Use it only when a command needs a credential or needs to run as ben. Check " +
				"supervisor_credentials for what a profile holds, and supervisor_policy_check if you suspect " +
				"a command will be refused.",
			Schema: func(profiles []string, def string) map[string]any {
				return object(map[string]any{
					"command": map[string]any{"type": "string", "description": "The exact command, run under bash. $VARS " +
						"resolve in that shell, where the credentials are."},
					"reason": map[string]any{"type": "string", "description": "Shown in Ben's approval prompt. Write it so he " +
						"can approve at a glance: \"Push the calendar fix to main\", not \"pushing\"."},
					"profile": profileSchema(profiles, def, "Which 1Password profile the credentials come from. Use the "+
						"default unless Ben says otherwise. Don't mix profiles in one command; split the work."),
					"credentials": map[string]any{"type": "array", "items": map[string]any{"type": "string"},
						"description": "Credential names to inject, e.g. [\"GITHUB_TOKEN\"]. Pass [] when the command needs " +
							"no secret: that injects nothing and skips 1Password. ALWAYS pass the field: omitting it " +
							"injects the profile's entire set and shows Ben a loud warning. Naming one the profile " +
							"lacks is an error that runs nothing."},
					"cwd": map[string]any{"type": "string", "description": "Working directory. Defaults to this session's " +
						"project directory. Set this instead of starting the command with `cd`."},
					"timeout_seconds": map[string]any{"type": "number", "default": defaultTimeoutS,
						"description": fmt.Sprintf("Wall clock for the command. Default %ds, ceiling %ds. On expiry it's "+
							"killed and you still get what it printed, flagged as timed out. Ben sees the duration.", defaultTimeoutS, maxTimeoutS)},
				}, "command", "reason")
			},
			run: s.execTool,
		},
		{
			Name: "supervisor_policy_check",
			Description: "Would a command be allowed? Doesn't run it, fetch credentials or ask anyone; free and " +
				"instant. Returns allow, ask or deny. Static policy only: grants, bypass and lockdown apply at call time.",
			Schema: func([]string, string) map[string]any {
				return object(map[string]any{"command": map[string]any{"type": "string", "description": "The command to evaluate."}}, "command")
			},
			run: s.policyCheckTool,
		},
		{
			Name: "supervisor_audit",
			Description: "Recent requests and their outcomes, oldest first: did that run, or is it still waiting? " +
				"Shows time, request ID, command, reason, profile, decision and exit code. Output isn't kept here: " +
				"to keep a large result, redirect it to a file in the command and read the file.",
			Schema: func([]string, string) map[string]any {
				return object(map[string]any{"limit": map[string]any{"type": "number", "default": 20, "description": "How many entries (up to 200)."}})
			},
			run: s.auditTool,
		},
		{
			Name: "supervisor_credentials",
			Description: "The credential NAMES in a profile (never values). No approval. Check what exists before " +
				"a supervisor_exec call instead of finding out from a failed one.",
			Schema: func(profiles []string, def string) map[string]any {
				return object(map[string]any{"profile": profileSchema(profiles, def, "The 1Password profile.")})
			},
			run: s.credentialsTool,
		},
		{
			Name:        "supervisor_status",
			Description: "The supervisor's mode (normal, bypass, lockdown), its 1Password profiles and its limits (command timeouts, output cap, approval window). No approval.",
			Schema:      func([]string, string) map[string]any { return object(map[string]any{}) },
			run: func(ctx context.Context, _ map[string]any) map[string]any {
				data, err := s.c.get(ctx, "/status")
				if err != nil {
					return unreachable(err)
				}
				b, _ := json.MarshalIndent(data, "", "  ")
				return textResult(string(b))
			},
		},
		{
			Name: "supervisor_request_grant",
			Description: "Ask Ben for a grant covering THIS session's supervisor_exec calls for some minutes, so a " +
				"series of related commands doesn't ask him each time. Ask for the narrowest scope that fits: the " +
				"exact credentials and the shortest time. \"*\" means any and is flagged to Ben; avoid it unless " +
				"he asked for broad access. He may approve it, narrow it or deny it; the result says what he " +
				"granted. A denial means stop: don't retry or work around it.",
			Schema: func(profiles []string, def string) map[string]any {
				return object(map[string]any{
					"profile": profileSchema(profiles, def, "The 1Password profile the commands use."),
					"credentials": map[string]any{
						"anyOf":       []any{map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, map[string]any{"type": "string", "enum": []string{"*"}}},
						"description": "Credential names the commands need ([] = none, \"*\" = any).",
					},
					"minutes": map[string]any{"type": "number", "description": "Duration, 1-480. Keep it short."},
					"reason":  map[string]any{"type": "string", "description": "What the work is for. Ben reads this on his phone."},
				}, "profile", "credentials", "minutes", "reason")
			},
			run: s.grantTool,
		},
		{
			Name: "supervisor_watch_pr",
			Description: "Call this right after you open a pull request on the eldar-os staging repo (a change to " +
				"this machine or this VM). Ben approves it by merging; the machine applies it within seconds. When " +
				"it's applied (or the apply fails, or the PR is closed unmerged), a message starting \"[eldar-os]\" " +
				"arrives in this session as your next prompt. Don't poll or wait in a loop: carry on, or stop " +
				"and wait for that message.",
			Schema: func([]string, string) map[string]any {
				return object(map[string]any{
					"pr":   map[string]any{"type": "number", "description": "The pull request number."},
					"repo": map[string]any{"type": "string", "description": "Repo name; defaults to the staging repo."},
				}, "pr")
			},
			run: s.watchPRTool,
		},
		{
			Name:        "notify_ben",
			Description: notifyDescription,
			Schema: func([]string, string) map[string]any {
				return object(map[string]any{
					"message":       map[string]any{"type": "string", "description": "The notification or question, one or two lines, leading with the actionable part."},
					"expects_reply": map[string]any{"type": "boolean", "description": "true when you're waiting on Ben's answer; Eldar sends it back into this session."},
					"origin":        map[string]any{"type": "string", "description": "Optional short label of who's sending, e.g. 'deploy script'. Defaults to this session's name."},
				}, "message")
			},
			run: s.notifyTool,
		},
	}
}

func (s *server) defaultProfile(ctx context.Context, args map[string]any) string {
	if p := str(args, "profile"); p != "" {
		return p
	}
	_, def := s.profiles(ctx)
	return def
}

func (s *server) session(payload map[string]any) {
	if s.c.sessionID != "" {
		payload["sessionId"], payload["sessionKey"] = s.c.sessionID, s.c.sessionKey
	}
	if c := approvalContext(); c != nil {
		payload["context"] = c
	}
}

func (s *server) execTool(ctx context.Context, args map[string]any) map[string]any {
	command := str(args, "command")
	if strings.TrimSpace(command) == "" {
		return errorText("command is required.")
	}
	timeoutS := defaultTimeoutS
	if t, ok := args["timeout_seconds"].(float64); ok {
		timeoutS = min(max(int(math.Round(t)), 1), maxTimeoutS)
	}
	payload := map[string]any{
		"script": command, "reason": str(args, "reason"), "timeoutSeconds": timeoutS,
		"profile": s.defaultProfile(ctx, args), "cwd": cwd(args),
	}
	// Sent whenever present, [] included: [] injects nothing, while leaving
	// it out injects the whole profile.
	if creds, ok := strList(args["credentials"]); ok {
		payload["credentials"] = creds
	}
	s.session(payload)
	data, err := s.c.post(ctx, "/exec", payload, httpTimeoutFor(timeoutS))
	if err != nil {
		return unreachable(err)
	}
	if _, ok := data["error"]; ok {
		return failure(data)
	}
	stdout, _ := data["stdout"].(string)
	stderr, _ := data["stderr"].(string)
	exit, _ := data["exitCode"].(float64)
	var parts []string
	if stdout != "" {
		parts = append(parts, stdout)
	}
	if stderr != "" {
		parts = append(parts, "--- stderr ---\n"+stderr)
	}
	if stdout == "" && stderr == "" {
		parts = append(parts, "(no output)")
	}
	if timedOut, _ := data["timedOut"].(bool); timedOut {
		parts = append(parts, fmt.Sprintf("--- TIMED OUT at %vs: the command was killed, but the output above is real "+
			"work it did first. Read it before deciding to re-run. Raise timeout_seconds if the work needs longer "+
			"(ceiling %ds). ---", data["timeoutSeconds"], maxTimeoutS))
	}
	if truncated, _ := data["truncated"].(bool); truncated {
		parts = append(parts, fmt.Sprintf("--- TRUNCATED: the command finished, but its output passed the %v-byte cap "+
			"(stdout %v bytes, stderr %v bytes). The rest is missing, so don't conclude anything from where this "+
			"stops. Re-run redirecting to a file and read that. ---", data["maxOutputBytes"], data["stdoutBytes"], data["stderrBytes"]))
	}
	if exit != 0 {
		parts = append(parts, fmt.Sprintf("[exit code: %d]", int(exit)))
	}
	r := textResult(strings.Join(parts, "\n\n"))
	if exit != 0 {
		r["isError"] = true
	}
	return r
}

func (s *server) policyCheckTool(ctx context.Context, args map[string]any) map[string]any {
	data, err := s.c.post(ctx, "/policy-check", map[string]any{"command": str(args, "command")}, 30*time.Second)
	if err != nil {
		return unreachable(err)
	}
	if e, ok := data["error"]; ok {
		return errorText(fmt.Sprint(e))
	}
	out := fmt.Sprintf("action      : %v\nwouldPrompt : %v", data["action"], data["wouldPrompt"])
	if n, ok := data["note"].(string); ok && n != "" {
		out += "\nnote        : " + n
	}
	return textResult(out)
}

func (s *server) auditTool(ctx context.Context, args map[string]any) map[string]any {
	limit := 20
	if l, ok := args["limit"].(float64); ok {
		limit = min(max(int(l), 1), 200)
	}
	data, err := s.c.get(ctx, fmt.Sprintf("/audit?limit=%d", limit))
	if err != nil {
		return unreachable(err)
	}
	if e, ok := data["error"]; ok {
		return errorText(fmt.Sprint(e))
	}
	entries, _ := data["entries"].([]any)
	if len(entries) == 0 {
		return textResult("(no audit entries)")
	}
	var rows []string
	for _, x := range entries {
		e, _ := x.(map[string]any)
		head := fmt.Sprintf("%v  #%v  %v", e["timestamp"], e["requestId"], e["decision"])
		if c, ok := e["exitCode"]; ok && c != nil {
			head += fmt.Sprintf(" exit=%v", c)
		}
		if p, _ := e["profile"].(string); p != "" {
			head += "  [" + p + "]"
		}
		lines := []string{head, fmt.Sprintf("  cmd   : %v", e["action"])}
		if r, _ := e["reason"].(string); r != "" {
			lines = append(lines, "  reason: "+r)
		}
		if w, _ := e["decisionReason"].(string); w != "" {
			lines = append(lines, "  why   : "+w)
		}
		rows = append(rows, strings.Join(lines, "\n"))
	}
	return textResult(fmt.Sprintf("%d entries (oldest first)\n\n%s", len(entries), strings.Join(rows, "\n\n")))
}

func (s *server) credentialsTool(ctx context.Context, args map[string]any) map[string]any {
	profile := s.defaultProfile(ctx, args)
	data, err := s.c.get(ctx, "/credentials?profile="+url.QueryEscape(profile))
	if err != nil {
		return unreachable(err)
	}
	if e, ok := data["error"]; ok {
		return errorText(fmt.Sprint(e))
	}
	names, _ := strList(data["credentials"])
	out := fmt.Sprintf("profile: %v (account: %v)\n", data["profile"], data["account"])
	if len(names) == 0 {
		return textResult(out + "  (none)")
	}
	return textResult(out + "  " + strings.Join(names, "\n  "))
}

func (s *server) grantTool(ctx context.Context, args map[string]any) map[string]any {
	if s.c.sessionID == "" {
		return errorText("This session has no CLAUDE_CODE_SESSION_ID, so there's nothing to scope a grant to.")
	}
	minutes, _ := args["minutes"].(float64)
	if minutes != math.Trunc(minutes) || minutes < 1 || minutes > 480 {
		return errorText("minutes must be a whole number from 1 to 480.")
	}
	payload := map[string]any{
		"target": "eldar", "minutes": minutes, "reason": str(args, "reason"),
		"profile": s.defaultProfile(ctx, args), "credentials": args["credentials"], "aws": []string{}, "cwd": cwd(args),
	}
	s.session(payload)
	data, err := s.c.post(ctx, "/grant-request", payload, 310*time.Second)
	if err != nil {
		return unreachable(err)
	}
	if _, ok := data["error"]; ok {
		return failure(data)
	}
	line, _ := data["message"].(string)
	if line == "" {
		line = fmt.Sprintf("Grant #%v made.", data["grantId"])
	}
	if n, _ := data["narrowed"].(bool); n {
		line += " Ben narrowed the scope: work within what was granted; anything outside it still asks."
	}
	if exp, ok := data["expiresAt"].(float64); ok && exp > 0 {
		line += " Expires " + time.UnixMilli(int64(exp)).UTC().Format(time.RFC3339) + "."
	}
	return textResult(line)
}

func (s *server) watchPRTool(ctx context.Context, args map[string]any) map[string]any {
	pr, _ := args["pr"].(float64)
	if pr < 1 || pr != math.Trunc(pr) {
		return errorText("pr must be the pull request's number.")
	}
	if s.c.sessionID == "" {
		return errorText("This session has no CLAUDE_CODE_SESSION_ID, so there's nothing to notify.")
	}
	payload := map[string]any{"pr": int(pr), "repo": str(args, "repo"), "paneId": os.Getenv("HERDR_PANE_ID")}
	s.session(payload)
	data, err := s.c.post(ctx, "/watch-pr", payload, 40*time.Second)
	if err != nil {
		return unreachable(err)
	}
	if _, ok := data["error"]; ok {
		return failure(data)
	}
	note := ""
	if os.Getenv("HERDR_PANE_ID") == "" {
		note = " This session isn't in a herdr pane, so the notice will go to Eldar instead."
	}
	return textResult(fmt.Sprintf("Watching %v#%v (%v). You'll get an [eldar-os] message here when it's applied.%s",
		data["repo"], data["pr"], data["title"], note))
}

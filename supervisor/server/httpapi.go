package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

const maxBody = 1 << 20

func sendJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func readJSON(r *http.Request, v any) error {
	b, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		b = []byte("{}")
	}
	return json.Unmarshal(b, v)
}

// agentHandler is the API agents call (the VM link). Never serves admin routes.
func (s *Supervisor) agentHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /exec", func(w http.ResponseWriter, r *http.Request) {
		var req ExecRequest
		if err := readJSON(r, &req); err != nil {
			sendJSON(w, 400, map[string]any{"error": "invalid JSON: " + err.Error(), "outcome": OutBadRequest})
			return
		}
		res := s.HandleExec(r.Context(), req)
		sendJSON(w, res.status, res.body)
	})
	grant := func(legacy string, shape func(*GrantRequestBody)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var b GrantRequestBody
			if err := readJSON(r, &b); err != nil {
				sendJSON(w, 400, map[string]any{"error": "invalid JSON: " + err.Error(), "outcome": OutBadRequest})
				return
			}
			if shape != nil {
				shape(&b)
			}
			res := s.HandleGrantRequest(b, legacy)
			sendJSON(w, res.status, res.body)
		}
	}
	mux.HandleFunc("POST /grant-request", grant("", nil))
	// Older clients: the same requests in their previous shapes.
	mux.HandleFunc("POST /bypass-request", grant("full grant request", func(b *GrantRequestBody) {
		b.Target, b.Profile, b.Credentials, b.Aws = "eldar", "*", json.RawMessage(`"*"`), json.RawMessage(`"*"`)
	}))
	mux.HandleFunc("POST /scoped-bypass-request", grant("scoped grant request", func(b *GrantRequestBody) {
		b.Target, b.Aws = "eldar", json.RawMessage(`[]`)
		if b.AwsProfile != "" {
			b.Aws, _ = json.Marshal([]string{b.AwsProfile})
		}
	}))
	mux.HandleFunc("GET /credentials", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("profile")
		if name == "" {
			sendJSON(w, 400, map[string]any{"error": "Missing 'profile' query parameter. " + s.profilesHint()})
			return
		}
		p, ok := s.profiles.Get(name)
		if !ok {
			sendJSON(w, 400, map[string]any{"error": fmt.Sprintf("Unknown profile '%s'. %s", name, s.profilesHint())})
			return
		}
		env, err := s.profiles.Read(r.Context(), p)
		if err != nil {
			sendJSON(w, 502, map[string]any{"error": fmt.Sprintf("Failed to fetch credentials for profile '%s': %v", p.Name, err)})
			return
		}
		sendJSON(w, 200, map[string]any{"profile": p.Name, "account": p.Account, "credentials": sortedKeys(env)})
	})
	mux.HandleFunc("GET /aws/profiles", func(w http.ResponseWriter, r *http.Request) {
		sendJSON(w, 200, map[string]any{"profiles": []string{}})
	})
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		mode, exp := s.modes.Status()
		profiles := []map[string]string{}
		for _, n := range s.profiles.Names() {
			p, _ := s.profiles.Get(n)
			profiles = append(profiles, map[string]string{"name": n, "account": p.Account, "environmentId": p.EnvID})
		}
		sendJSON(w, 200, map[string]any{
			"mode": mode, "expiresAt": exp, "requestCount": s.reqID.Load(),
			"op":    map[string]any{"profiles": profiles, "defaultProfile": s.cfg.DefaultProfile, "warnings": nonNil(s.profiles.Warnings)},
			"hosts": s.agentHosts(),
			"limits": map[string]any{
				"defaultCommandTimeoutS": int(s.cfg.DefaultCommandTimeout.Seconds()),
				"maxCommandTimeoutS":     int(s.cfg.MaxCommandTimeout.Seconds()),
				"maxOutputBytes":         s.cfg.MaxOutputBytes,
				"approvalTimeoutMs":      s.cfg.ApprovalTimeout.Milliseconds(),
			},
		})
	})
	// "Would this be allowed?" without running anything: static policy only.
	mux.HandleFunc("POST /policy-check", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		}
		readJSON(r, &b)
		cmd := strings.TrimSpace(b.Command)
		if len(b.Args) > 0 {
			cmd = strings.TrimSpace(b.Command + " " + strings.Join(b.Args, " "))
		}
		if cmd == "" {
			sendJSON(w, 400, map[string]any{"error": "Provide 'command' (and optionally 'args')", "outcome": OutBadRequest})
			return
		}
		action, err := s.policy.Evaluate(cmd, "")
		if err != nil {
			sendJSON(w, 500, map[string]any{"error": err.Error(), "outcome": OutInternal})
			return
		}
		sendJSON(w, 200, map[string]any{"command": cmd, "action": action, "wouldPrompt": action == Ask,
			"note": "Static policy only — bypasses, remembered approvals and lockdown are evaluated at call time."})
	})
	// Recent outcomes, without command output (which may hold others' data).
	mux.HandleFunc("GET /audit", func(w http.ResponseWriter, r *http.Request) {
		limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
		if err != nil {
			limit = 20
		}
		limit = min(max(limit, 1), 200)
		entries := []map[string]any{}
		for _, e := range s.audit.Tail(limit) {
			m := map[string]any{"timestamp": e.Timestamp, "requestId": e.RequestID, "action": e.Action,
				"reason": e.Reason, "profile": e.Profile, "decision": e.Decision, "decisionReason": e.DecisionReason, "mode": e.Mode}
			if e.Result != nil {
				m["exitCode"] = e.Result.ExitCode
			}
			entries = append(entries, m)
		}
		sendJSON(w, 200, map[string]any{"count": len(entries), "limit": limit, "entries": entries})
	})
	mux.HandleFunc("GET /github/token", s.handleGitHubToken)
	mux.HandleFunc("POST /watch-pr", s.handleWatchPR)
	s.chatRoutes(mux)
	s.relayRoutes(mux)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		sendJSON(w, 404, map[string]any{"error": "Not found"})
	})
	return mux
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}

// Package web serves the eldar-proxy web UI on the tailnet.
//
// The server joins the tailnet itself (tsnet) as its own node and listens only
// there, over HTTPS. Every request is checked with Tailscale's WhoIs: only
// untagged devices owned by the allowed user get through. The agent's node is
// tagged (tag:eldar), so it is refused here even if an ACL ever let it reach
// the port. Mutating requests also require a custom header, which a cross-site
// page can't send without a CORS preflight this server never answers.
//
// Behind that, it's a relay onto the same admin API (package api) the TUI uses,
// plus the derived view (package view), so both UIs show the same thing.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/tsnet"

	"github.com/benjaminwood/eldar-os/supervisor/api"
	"github.com/benjaminwood/eldar-os/supervisor/view"
)

//go:embed static
var staticFS embed.FS

// Config for Run.
type Config struct {
	Client      *api.Client
	Hostname    string // tailnet machine name
	StateDir    string // tsnet state, push keys and subscriptions
	AuthKeyFile string // one-time auth key; only needed until the node is registered
	AllowedUser string // tailnet login allowed to use the UI
	Listen      string // optional plain-HTTP listener for local development, e.g. 127.0.0.1:8080
	StaticDir   string // serve UI files from disk instead of the embedded copy (live editing)
	// BrowserDevice is the tailnet machine whose browser Eldar can drive (Arc on
	// Ben's Mac). While any Arc window is approved, that device is refused, so a
	// page Eldar controls can't answer its own approvals.
	BrowserDevice string
}

type server struct {
	cfg    Config
	lc     *local.Client
	push   *pusher
	keys   *passkeys // nil until the tailnet HTTPS name is known
	static fs.FS
}

// Run serves until ctx ends.
func Run(ctx context.Context, cfg Config) error {
	if cfg.AllowedUser == "" {
		return errors.New("an allowed tailnet user is required")
	}
	srv := &server{cfg: cfg}
	srv.static, _ = fs.Sub(staticFS, "static")
	if cfg.StaticDir != "" {
		srv.static = os.DirFS(cfg.StaticDir)
		log.Printf("serving UI files from %s", cfg.StaticDir)
	}
	var err error
	if srv.push, err = newPusher(cfg.StateDir, cfg.Client, cfg.Hostname); err != nil {
		return fmt.Errorf("push: %w", err)
	}
	go srv.push.watch(ctx)

	if cfg.Listen != "" {
		// Development only: no tailnet identity, so no auth. Bind to loopback.
		log.Printf("dev listener on http://%s (no auth — loopback only)", cfg.Listen)
		return http.ListenAndServe(cfg.Listen, srv.routes(false))
	}

	ts := &tsnet.Server{
		Hostname: cfg.Hostname,
		Dir:      cfg.StateDir,
		Logf:     func(string, ...any) {},
		UserLogf: log.Printf,
	}
	if b, err := os.ReadFile(cfg.AuthKeyFile); err == nil {
		ts.AuthKey = strings.TrimSpace(string(b))
	}
	defer ts.Close()
	status, err := ts.Up(ctx)
	if err != nil {
		return fmt.Errorf("tailnet: %w", err)
	}
	// Registered: the node's identity now lives in StateDir, so the one-time
	// key has no further use. Remove it rather than leave a credential around.
	if ts.AuthKey != "" {
		_ = os.Remove(cfg.AuthKeyFile)
	}
	if srv.lc, err = ts.LocalClient(); err != nil {
		return err
	}
	domains := ts.CertDomains()
	if len(domains) == 0 {
		return errors.New("tailnet has no HTTPS certificate domain — enable HTTPS in the Tailscale admin console")
	}
	srv.push.origin = "https://" + domains[0]
	if srv.keys, err = newPasskeys(cfg.StateDir, domains[0]); err != nil {
		return fmt.Errorf("passkeys: %w", err)
	}
	log.Printf("tailnet up as %s (%v) — serving https://%s", cfg.Hostname, status.TailscaleIPs, domains[0])

	ln, err := ts.ListenTLS("tcp", ":443")
	if err != nil {
		return err
	}
	hs := &http.Server{Handler: srv.routes(true), ReadHeaderTimeout: 10 * time.Second}
	go func() { <-ctx.Done(); hs.Close() }()
	if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *server) routes(auth bool) http.Handler {
	mux := http.NewServeMux()
	files := http.FileServer(http.FS(s.static))

	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("GET /icon/{file}", s.icon)
	mux.HandleFunc("GET /sw.js", func(w http.ResponseWriter, r *http.Request) {
		// The service worker must be revalidated so updates reach installed apps.
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
	// Revalidate UI files on every load (cheap 304s) so a fix is never hidden
	// behind a stale cached copy.
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})))
	mux.Handle("GET /manifest.webmanifest", files)

	mux.HandleFunc("GET /api/state", s.state)
	mux.HandleFunc("GET /api/events", s.events)
	mux.HandleFunc("GET /api/audit", s.audit)
	mux.HandleFunc("GET /api/audit/entry", s.auditEntry)
	mux.HandleFunc("GET /api/credentials", s.credentials)
	mux.HandleFunc("POST /api/decide", s.decide)
	mux.HandleFunc("POST /api/mode", s.mode)
	mux.HandleFunc("POST /api/grants", s.addGrant)
	mux.HandleFunc("DELETE /api/grants/{id}", s.revokeGrant)
	mux.HandleFunc("POST /api/grant-requests/{id}/approve", s.approveGrantRequest)
	mux.HandleFunc("DELETE /api/browser-grants/{id}", s.revokeBrowserGrant)
	mux.HandleFunc("POST /api/sessions/{id}/unpin", s.unpin)
	mux.HandleFunc("POST /api/settings", s.settings)
	mux.HandleFunc("POST /api/cache/clear", s.cacheClear)
	mux.HandleFunc("GET /api/chat", s.chatList)
	mux.HandleFunc("POST /api/chat", s.chatSend)
	mux.HandleFunc("POST /api/chat/read", s.chatRead)
	mux.HandleFunc("POST /api/mcp/register", s.mcpRegister)
	mux.HandleFunc("POST /api/mcp/callback", s.mcpCallback)
	mux.HandleFunc("DELETE /api/mcp/{name}", s.mcpRemove)
	mux.HandleFunc("POST /api/herdr/launch", s.herdrLaunch)
	mux.HandleFunc("POST /api/herdr/close", s.herdrClose)
	mux.HandleFunc("POST /api/herdr/shell", s.herdrShell)
	mux.HandleFunc("POST /api/mac/test", s.macTest)
	mux.HandleFunc("POST /api/mac/allow", s.macAllow(false))
	mux.HandleFunc("DELETE /api/mac/allow", s.macAllow(true))
	mux.HandleFunc("POST /api/crons", s.cronAllow)
	mux.HandleFunc("DELETE /api/crons/{id}", s.cronRevoke)
	mux.HandleFunc("GET /api/push/key", s.pushKey)
	mux.HandleFunc("GET /api/passkey", s.passkeyStatus)
	mux.HandleFunc("POST /api/passkey/register/begin", s.passkeyBegin("register"))
	mux.HandleFunc("POST /api/passkey/register/finish", s.passkeyFinish("register"))
	mux.HandleFunc("POST /api/passkey/unlock/begin", s.passkeyBegin("unlock"))
	mux.HandleFunc("POST /api/passkey/unlock/finish", s.passkeyFinish("unlock"))
	mux.HandleFunc("POST /api/passkey/lock", s.passkeyLock)
	mux.HandleFunc("GET /api/terminal", s.terminal)
	mux.HandleFunc("POST /api/push/subscribe", s.pushSubscribe)
	mux.HandleFunc("POST /api/push/unsubscribe", s.pushUnsubscribe)
	mux.HandleFunc("POST /api/push/test", s.pushTest)

	var h http.Handler = mux
	h = guard(h)
	if auth {
		h = s.requireUnlock(h)
		h = s.whois(h)
	}
	return securityHeaders(h)
}

type ctxKey string

// whois admits only untagged devices owned by the allowed user.
func (s *server) whois(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		who, err := s.lc.WhoIs(r.Context(), r.RemoteAddr)
		if err != nil || who.Node == nil || who.UserProfile == nil {
			http.Error(w, "unknown peer", http.StatusForbidden)
			return
		}
		if len(who.Node.Tags) > 0 || !strings.EqualFold(who.UserProfile.LoginName, s.cfg.AllowedUser) {
			log.Printf("refused %s (%s, tags %v)", who.Node.ComputedName, who.UserProfile.LoginName, who.Node.Tags)
			http.Error(w, "not allowed", http.StatusForbidden)
			return
		}
		if s.isBrowserDevice(who.Node.Name) && s.browserDriven(r.Context()) {
			log.Printf("refused %s: Eldar is driving its browser", who.Node.ComputedName)
			http.Error(w, "Eldar is driving the browser on this device. Use your phone or the TUI until its Arc window ends.", http.StatusForbidden)
			return
		}
		ctx := context.WithValue(r.Context(), ctxKey("device"), who.Node.ComputedName)
		ctx = api.WithClientName(ctx, "web · "+who.Node.ComputedName)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// isBrowserDevice: node is the configured browser machine (FQDN, first label).
func (s *server) isBrowserDevice(fqdn string) bool {
	name, _, _ := strings.Cut(fqdn, ".")
	return s.cfg.BrowserDevice != "" && strings.EqualFold(name, s.cfg.BrowserDevice)
}

// browserDriven reports an approved Arc window. Unknown counts as yes.
func (s *server) browserDriven(ctx context.Context) bool {
	st, err := s.cfg.Client.State(ctx)
	return err != nil || len(st.BrowserGrants) > 0
}

// guard blocks cross-site writes: every non-GET needs X-Eldar-Proxy, and a
// browser's own Origin / Sec-Fetch-Site must say same-origin when present.
func guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			http.Error(w, "no", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if r.Header.Get("X-Eldar-Proxy") != "1" {
				http.Error(w, "missing X-Eldar-Proxy header", http.StatusForbidden)
				return
			}
			if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
				http.Error(w, "cross-site request", http.StatusForbidden)
				return
			}
			if o := r.Header.Get("Origin"); o != "" {
				if u, err := url.Parse(o); err != nil || u.Host != r.Host {
					http.Error(w, "cross-origin request", http.StatusForbidden)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; font-src https://fonts.gstatic.com; connect-src 'self' wss://"+r.Host+"; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

func (s *server) index(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	device, _ := r.Context().Value(ctxKey("device")).(string)
	tmpl, err := template.ParseFS(s.static, "index.html")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	_ = tmpl.Execute(w, map[string]any{
		"ThemeVars": template.CSS(view.CSSVars()),
		"Device":    device,
	})
}

// payload is what the browser renders: the raw state plus derived sessions.
type payload struct {
	State    api.State      `json:"state"`
	Sessions []view.Session `json:"sessions"`
}

func newPayload(st api.State) payload {
	return payload{State: st, Sessions: view.Sessions(st)}
}

func (s *server) state(w http.ResponseWriter, r *http.Request) {
	st, err := s.cfg.Client.State(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, newPayload(st))
}

// events relays the admin stream to the browser as derived payloads.
func (s *server) events(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	fl.Flush()
	token, device := unlockToken(r), deviceOf(r)
	for ev := range s.cfg.Client.Subscribe(r.Context()) {
		if s.keys != nil {
			if _, ok := s.keys.unlocked(token, device, false); !ok {
				io.WriteString(w, "event: locked\ndata: {}\n\n")
				fl.Flush()
				return
			}
		}
		var frame string
		if ev.State != nil {
			b, _ := json.Marshal(newPayload(*ev.State))
			frame = "event: state\ndata: " + string(b) + "\n\n"
		} else {
			msg := "proxy unreachable"
			if ev.Err != nil {
				msg = ev.Err.Error()
			}
			b, _ := json.Marshal(map[string]string{"error": msg})
			frame = "event: down\ndata: " + string(b) + "\n\n"
		}
		if _, err := io.WriteString(w, frame); err != nil {
			return
		}
		fl.Flush()
	}
}

func (s *server) audit(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 150
	}
	entries, err := s.cfg.Client.Audit(r.Context(), limit)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"entries": entries})
}

func (s *server) auditEntry(w http.ResponseWriter, r *http.Request) {
	rid, _ := strconv.Atoi(r.URL.Query().Get("requestId"))
	e, err := s.cfg.Client.AuditEntryDetail(r.Context(), r.URL.Query().Get("ts"), rid)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, e)
}

func (s *server) credentials(w http.ResponseWriter, r *http.Request) {
	names, err := s.cfg.Client.Credentials(r.Context(), r.URL.Query().Get("profile"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"credentials": names})
}

func readBody[T any](w http.ResponseWriter, r *http.Request) (T, bool) {
	var v T
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&v); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad JSON: " + err.Error()})
		return v, false
	}
	return v, true
}

func (s *server) decide(w http.ResponseWriter, r *http.Request) {
	b, ok := readBody[struct {
		IDs      []int        `json:"ids"`
		Decision api.Decision `json:"decision"`
	}](w, r)
	if !ok {
		return
	}
	var errs []string
	for _, id := range b.IDs {
		if err := s.cfg.Client.Decide(r.Context(), id, b.Decision); err != nil {
			errs = append(errs, fmt.Sprintf("#%d: %s", id, err))
		}
	}
	if len(errs) > 0 {
		writeJSON(w, 409, map[string]string{"error": strings.Join(errs, "; ")})
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *server) mode(w http.ResponseWriter, r *http.Request) {
	b, ok := readBody[struct {
		Mode    api.Mode `json:"mode"`
		Minutes *int     `json:"minutes"`
	}](w, r)
	if !ok {
		return
	}
	ok2(w, s.cfg.Client.SetMode(r.Context(), b.Mode, b.Minutes))
}

func (s *server) addGrant(w http.ResponseWriter, r *http.Request) {
	b, ok := readBody[struct {
		api.GrantInput
		Approve int `json:"approve"`
	}](w, r)
	if !ok {
		return
	}
	g, err := s.cfg.Client.AddGrant(r.Context(), b.GrantInput)
	if err != nil {
		fail(w, err)
		return
	}
	if b.Approve != 0 {
		if err := s.cfg.Client.Decide(r.Context(), b.Approve, api.Approve); err != nil {
			writeJSON(w, 409, map[string]string{"error": fmt.Sprintf("grant #%d created, but #%d: %s", g.ID, b.Approve, err)})
			return
		}
	}
	writeJSON(w, 200, g)
}

func pathID(w http.ResponseWriter, r *http.Request) (int, bool) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad id"})
		return 0, false
	}
	return id, true
}

func (s *server) revokeGrant(w http.ResponseWriter, r *http.Request) {
	if id, ok := pathID(w, r); ok {
		ok2(w, s.cfg.Client.RevokeGrant(r.Context(), id))
	}
}

func (s *server) approveGrantRequest(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	in, ok := readBody[api.GrantInput](w, r)
	if !ok {
		return
	}
	ok2(w, s.cfg.Client.ApproveGrantRequest(r.Context(), id, in))
}

func (s *server) revokeBrowserGrant(w http.ResponseWriter, r *http.Request) {
	if id, ok := pathID(w, r); ok {
		ok2(w, s.cfg.Client.RevokeBrowserGrant(r.Context(), id))
	}
}

func (s *server) unpin(w http.ResponseWriter, r *http.Request) {
	ok2(w, s.cfg.Client.UnpinSession(r.Context(), r.PathValue("id")))
}

func (s *server) settings(w http.ResponseWriter, r *http.Request) {
	b, ok := readBody[struct {
		MobilePush bool `json:"mobilePush"`
	}](w, r)
	if !ok {
		return
	}
	st, err := s.cfg.Client.SetMobilePush(r.Context(), b.MobilePush)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, st)
}

func (s *server) mcpRegister(w http.ResponseWriter, r *http.Request) {
	b, ok := readBody[struct{ Name, URL string }](w, r)
	if !ok {
		return
	}
	link, err := s.cfg.Client.RegisterMCP(r.Context(), b.Name, b.URL)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"authUrl": link})
}

func (s *server) mcpCallback(w http.ResponseWriter, r *http.Request) {
	b, ok := readBody[struct{ URL string }](w, r)
	if !ok {
		return
	}
	name, err := s.cfg.Client.CompleteMCP(r.Context(), b.URL)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"name": name})
}

func (s *server) mcpRemove(w http.ResponseWriter, r *http.Request) {
	ok2(w, s.cfg.Client.RemoveMCP(r.Context(), r.PathValue("name")))
}

func (s *server) herdrLaunch(w http.ResponseWriter, r *http.Request) {
	in, ok := readBody[api.LaunchInput](w, r)
	if !ok {
		return
	}
	in.Session = "" // the web UI only ever manages Ben's real session
	out, err := s.cfg.Client.LaunchClaude(r.Context(), in)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (s *server) herdrShell(w http.ResponseWriter, r *http.Request) {
	b, ok := readBody[struct {
		WorkspaceID string `json:"workspaceId"`
		Name        string `json:"name"`
		Cwd         string `json:"cwd"`
	}](w, r)
	if !ok {
		return
	}
	out, err := s.cfg.Client.OpenShell(r.Context(), b.WorkspaceID, b.Name, b.Cwd)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (s *server) herdrClose(w http.ResponseWriter, r *http.Request) {
	b, ok := readBody[struct {
		PaneID string `json:"paneId"`
	}](w, r)
	if !ok {
		return
	}
	ok2(w, s.cfg.Client.CloseClaude(r.Context(), b.PaneID, ""))
}

func (s *server) macTest(w http.ResponseWriter, r *http.Request) {
	t, err := s.cfg.Client.TestMac(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, t)
}

func (s *server) macAllow(remove bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, ok := readBody[struct {
			Pattern string `json:"pattern"`
		}](w, r)
		if !ok {
			return
		}
		ok2(w, s.cfg.Client.MacAllow(r.Context(), b.Pattern, remove))
	}
}

func (s *server) chatList(w http.ResponseWriter, r *http.Request) {
	q := url.Values{}
	for _, k := range []string{"after", "before", "limit"} {
		if v := r.URL.Query().Get(k); v != "" {
			q.Set(k, v)
		}
	}
	out, err := s.cfg.Client.Chat(r.Context(), q)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (s *server) chatSend(w http.ResponseWriter, r *http.Request) {
	b, ok := readBody[struct {
		Text string `json:"text"`
	}](w, r)
	if !ok {
		return
	}
	out, err := s.cfg.Client.SendChat(r.Context(), b.Text)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (s *server) chatRead(w http.ResponseWriter, r *http.Request) {
	b, ok := readBody[struct {
		UpTo int `json:"upTo"`
	}](w, r)
	if !ok {
		return
	}
	ok2(w, s.cfg.Client.ChatRead(r.Context(), b.UpTo))
}

func (s *server) cacheClear(w http.ResponseWriter, r *http.Request) {
	b, ok := readBody[struct {
		Name string `json:"name"`
	}](w, r)
	if !ok {
		return
	}
	removed, err := s.cfg.Client.ClearCache(r.Context(), b.Name)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"removed": removed})
}

func (s *server) cronAllow(w http.ResponseWriter, r *http.Request) {
	b, ok := readBody[struct{ ID, Name string }](w, r)
	if !ok {
		return
	}
	ok2(w, s.cfg.Client.AllowCron(r.Context(), b.ID, b.Name))
}

func (s *server) cronRevoke(w http.ResponseWriter, r *http.Request) {
	ok2(w, s.cfg.Client.RevokeCron(r.Context(), r.PathValue("id")))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, err error) {
	status := 502
	var ae *api.APIError
	if errors.As(err, &ae) {
		status = ae.Status
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func ok2(w http.ResponseWriter, err error) {
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

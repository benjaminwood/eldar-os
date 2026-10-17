package web

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

// Passkeys gate the whole web app behind Face ID / Touch ID (WebAuthn with
// user verification required). Tailnet identity says "this is Ben's phone";
// a passkey assertion says "and Ben is holding it". Nothing under /api but the
// passkey ceremony itself answers without it.
//
//   - The first passkey can be registered by any device the tailnet admits.
//     Every later one needs a fresh unlock from an existing passkey, so an
//     unlocked phone alone can't enrol a new key. Reset by deleting
//     passkeys.json in the state dir.
//   - A successful check issues an unlock token bound to that device, sent as
//     an HttpOnly cookie (the event stream can't carry headers). It lapses
//     after unlockIdle without a request, or unlockMax after the check.

const (
	unlockIdle   = 15 * time.Minute
	unlockMax    = 12 * time.Hour
	flowTTL      = 3 * time.Minute
	unlockCookie = "eldar_unlock"
)

type storedKey struct {
	Credential webauthn.Credential `json:"credential"`
	Device     string              `json:"device"`
	Created    time.Time           `json:"created"`
	LastUsed   time.Time           `json:"lastUsed"`
}

type passkeyFile struct {
	UserID []byte      `json:"userId"`
	Keys   []storedKey `json:"keys"`
}

type flow struct {
	session webauthn.SessionData
	kind    string // "register" | "unlock"
	device  string
	expires time.Time
}

type unlock struct {
	device   string
	expires  time.Time // hard limit
	lastUsed time.Time
}

type passkeys struct {
	mu      sync.Mutex
	path    string
	data    passkeyFile
	wa      *webauthn.WebAuthn
	flows   map[string]flow
	unlocks map[string]unlock
}

// benUser is the single WebAuthn account: Ben.
type benUser struct{ p *passkeys }

func (u benUser) WebAuthnID() []byte          { return u.p.data.UserID }
func (u benUser) WebAuthnName() string        { return "ben" }
func (u benUser) WebAuthnDisplayName() string { return "Ben" }
func (u benUser) WebAuthnCredentials() []webauthn.Credential {
	out := make([]webauthn.Credential, len(u.p.data.Keys))
	for i, k := range u.p.data.Keys {
		out[i] = k.Credential
	}
	return out
}

func randomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func newPasskeys(dir, host string) (*passkeys, error) {
	wa, err := webauthn.New(&webauthn.Config{
		RPID:          host,
		RPDisplayName: "Eldar",
		RPOrigins:     []string{"https://" + host},
	})
	if err != nil {
		return nil, err
	}
	p := &passkeys{path: filepath.Join(dir, "passkeys.json"), wa: wa, flows: map[string]flow{}, unlocks: map[string]unlock{}}
	if b, err := os.ReadFile(p.path); err == nil {
		if err := json.Unmarshal(b, &p.data); err != nil {
			return nil, err
		}
	}
	if len(p.data.UserID) == 0 {
		p.data.UserID = make([]byte, 32)
		_, _ = rand.Read(p.data.UserID)
		if err := writeFile0600(p.path, p.data); err != nil {
			return nil, err
		}
	}
	return p, nil
}

func (p *passkeys) sweepLocked() {
	now := time.Now()
	for k, f := range p.flows {
		if now.After(f.expires) {
			delete(p.flows, k)
		}
	}
	for k, u := range p.unlocks {
		if now.After(u.expires) || now.Sub(u.lastUsed) > unlockIdle {
			delete(p.unlocks, k)
		}
	}
}

// unlocked reports whether token is a live unlock for device, and when it
// lapses if unused. touch counts this request as use.
func (p *passkeys) unlocked(token, device string, touch bool) (time.Time, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sweepLocked()
	u, ok := p.unlocks[token]
	if !ok || u.device != device {
		return time.Time{}, false
	}
	if touch {
		u.lastUsed = time.Now()
		p.unlocks[token] = u
	}
	return u.lastUsed.Add(unlockIdle), true
}

func (p *passkeys) issueUnlockLocked(device string) (string, time.Time) {
	tok := randomToken(32)
	now := time.Now()
	p.unlocks[tok] = unlock{device: device, expires: now.Add(unlockMax), lastUsed: now}
	return tok, now.Add(unlockIdle)
}

func (p *passkeys) lock(token string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.unlocks, token)
}

// unlockToken is the request's unlock: the cookie, else the header.
func unlockToken(r *http.Request) string {
	if c, err := r.Cookie(unlockCookie); err == nil && c.Value != "" {
		return c.Value
	}
	return r.Header.Get("X-Eldar-Unlock")
}

func setUnlockCookie(w http.ResponseWriter, tok string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: unlockCookie, Value: tok, Path: "/", MaxAge: maxAge, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
}

// requireUnlock refuses /api requests without a live unlock for this device,
// except the passkey ceremony. Fails closed when passkeys are unavailable.
// The event stream doesn't count as use, so an open, idle page still locks.
func (s *server) requireUnlock(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if !strings.HasPrefix(path, "/api/") || path == "/api/passkey" || strings.HasPrefix(path, "/api/passkey/") {
			next.ServeHTTP(w, r)
			return
		}
		if s.keys == nil {
			writeJSON(w, 503, map[string]any{"error": "Face ID needs the tailnet HTTPS name", "locked": true})
			return
		}
		if _, ok := s.keys.unlocked(unlockToken(r), deviceOf(r), path != "/api/events"); !ok {
			writeJSON(w, 401, map[string]any{"error": "locked — confirm it's you with Face ID", "locked": true})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func deviceOf(r *http.Request) string {
	d, _ := r.Context().Value(ctxKey("device")).(string)
	return d
}

// ── handlers ───────────────────────────────────────────────────────────────

func (s *server) passkeyStatus(w http.ResponseWriter, r *http.Request) {
	if s.keys == nil {
		writeJSON(w, 200, map[string]any{"available": false})
		return
	}
	p := s.keys
	p.mu.Lock()
	devices := make([]map[string]any, 0, len(p.data.Keys))
	for _, k := range p.data.Keys {
		devices = append(devices, map[string]any{"device": k.Device, "created": k.Created, "lastUsed": k.LastUsed})
	}
	n := len(p.data.Keys)
	p.mu.Unlock()
	exp, ok := p.unlocked(unlockToken(r), deviceOf(r), false)
	out := map[string]any{"available": true, "registered": n, "devices": devices, "unlocked": ok}
	if ok {
		out["unlockedUntil"] = exp.UnixMilli()
	}
	writeJSON(w, 200, out)
}

func (s *server) passkeyBegin(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := s.keys
		if p == nil {
			writeJSON(w, 503, map[string]string{"error": "passkeys need the tailnet HTTPS name"})
			return
		}
		device := deviceOf(r)
		p.mu.Lock()
		defer p.mu.Unlock()
		p.sweepLocked()
		user := benUser{p}
		var options any
		var session *webauthn.SessionData
		var err error
		switch kind {
		case "register":
			if len(p.data.Keys) > 0 {
				u, ok := p.unlocks[unlockToken(r)]
				if !ok || u.device != device {
					writeJSON(w, 403, map[string]string{"error": "adding another passkey needs Face ID from an existing one first"})
					return
				}
			}
			var exclude []protocol.CredentialDescriptor
			for _, c := range user.WebAuthnCredentials() {
				exclude = append(exclude, c.Descriptor())
			}
			options, session, err = p.wa.BeginRegistration(user,
				webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
					AuthenticatorAttachment: protocol.Platform,
					ResidentKey:             protocol.ResidentKeyRequirementPreferred,
					UserVerification:        protocol.VerificationRequired,
				}),
				webauthn.WithExclusions(exclude),
			)
		default:
			if len(p.data.Keys) == 0 {
				writeJSON(w, 409, map[string]string{"error": "no passkey yet — set up Face ID first"})
				return
			}
			options, session, err = p.wa.BeginLogin(user, webauthn.WithUserVerification(protocol.VerificationRequired))
		}
		if err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		id := randomToken(18)
		p.flows[id] = flow{session: *session, kind: kind, device: device, expires: time.Now().Add(flowTTL)}
		writeJSON(w, 200, map[string]any{"flow": id, "options": options})
	}
}

func (s *server) passkeyFinish(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := s.keys
		if p == nil {
			writeJSON(w, 503, map[string]string{"error": "passkeys unavailable"})
			return
		}
		device := deviceOf(r)
		p.mu.Lock()
		defer p.mu.Unlock()
		p.sweepLocked()
		id := r.URL.Query().Get("flow")
		f, ok := p.flows[id]
		delete(p.flows, id)
		if !ok || f.kind != kind || f.device != device {
			writeJSON(w, 400, map[string]string{"error": "that Face ID request expired — try again"})
			return
		}
		user := benUser{p}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		switch kind {
		case "register":
			cred, err := p.wa.FinishRegistration(user, f.session, r)
			if err != nil {
				writeJSON(w, 400, map[string]string{"error": "passkey not accepted: " + describe(err)})
				return
			}
			if !cred.Flags.UserVerified {
				writeJSON(w, 400, map[string]string{"error": "the device didn't verify you (Face ID / passcode)"})
				return
			}
			now := time.Now()
			p.data.Keys = append(p.data.Keys, storedKey{Credential: *cred, Device: device, Created: now, LastUsed: now})
			if err := writeFile0600(p.path, p.data); err != nil {
				writeJSON(w, 500, map[string]string{"error": "saving passkey: " + err.Error()})
				return
			}
		default:
			cred, err := p.wa.FinishLogin(user, f.session, r)
			if err != nil {
				writeJSON(w, 400, map[string]string{"error": "Face ID check failed: " + describe(err)})
				return
			}
			if !cred.Flags.UserVerified {
				writeJSON(w, 400, map[string]string{"error": "the device didn't verify you (Face ID / passcode)"})
				return
			}
			for i := range p.data.Keys {
				if string(p.data.Keys[i].Credential.ID) == string(cred.ID) {
					p.data.Keys[i].Credential.Authenticator = cred.Authenticator
					p.data.Keys[i].LastUsed = time.Now()
				}
			}
			_ = writeFile0600(p.path, p.data)
		}
		tok, exp := p.issueUnlockLocked(device)
		setUnlockCookie(w, tok, int(unlockMax/time.Second))
		writeJSON(w, 200, map[string]any{"expiresAt": exp.UnixMilli()})
	}
}

// passkeyLock ends this device's unlock now.
func (s *server) passkeyLock(w http.ResponseWriter, r *http.Request) {
	if s.keys != nil {
		s.keys.lock(unlockToken(r))
	}
	setUnlockCookie(w, "", -1)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func describe(err error) string {
	var pe *protocol.Error
	if errors.As(err, &pe) && pe.Details != "" {
		return pe.Details
	}
	return err.Error()
}

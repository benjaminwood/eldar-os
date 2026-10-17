// apply-hook starts an apply as soon as GitHub says a watched repo changed,
// so a merge is live in seconds instead of at the apply timer's next run.
// docs/decisions/0012-cycle-time.md
//
// It joins the tailnet as its own node (tsnet) and takes GitHub webhooks over
// Tailscale Funnel: public HTTPS, with no access to the tailnet. A delivery
// is only a ping. Its signature (X-Hub-Signature-256, HMAC with a shared
// secret) is checked so strangers can't make the machine fetch, but nothing
// in the payload is used: the apply fetches from GitHub itself and checks
// signatures before applying anything (0005).
//
// Configured by environment: HOOK_HOSTNAME (tailnet name), HOOK_STATE_DIR
// (tsnet state; a one-time auth key in authkey there), HOOK_SECRET_FILE,
// HOOK_UNIT (default eldar-os-apply.service).
package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"tailscale.com/tsnet"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	hostname := env("HOOK_HOSTNAME", "eldar-os-hook")
	stateDir := env("HOOK_STATE_DIR", "/var/lib/eldar-os-hook")
	secret, err := os.ReadFile(env("HOOK_SECRET_FILE", ""))
	if err != nil || len(strings.TrimSpace(string(secret))) == 0 {
		log.Fatalf("apply-hook: no webhook secret: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	t := &trigger{unit: env("HOOK_UNIT", "eldar-os-apply.service")}
	h := &handler{secret: []byte(strings.TrimSpace(string(secret))), fire: t.fire}

	ts := &tsnet.Server{Hostname: hostname, Dir: stateDir, Logf: func(string, ...any) {}, UserLogf: log.Printf}
	keyFile := filepath.Join(stateDir, "authkey")
	if b, err := os.ReadFile(keyFile); err == nil {
		ts.AuthKey = strings.TrimSpace(string(b))
	}
	defer ts.Close()
	if _, err := ts.Up(ctx); err != nil {
		log.Fatalf("apply-hook: tailnet: %v", err)
	}
	if ts.AuthKey != "" {
		os.Remove(keyFile) // registered; the one-time key has no further use
	}
	ln, err := ts.ListenFunnel("tcp", ":443", tsnet.FunnelOnly())
	if err != nil {
		log.Fatalf("apply-hook: funnel: %v", err)
	}
	if d := ts.CertDomains(); len(d) > 0 {
		log.Printf("apply-hook: taking webhooks at https://%s/github", d[0])
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second}
	go func() { <-ctx.Done(); srv.Close() }()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("apply-hook: %v", err)
	}
}

type handler struct {
	secret []byte
	fire   func(reason string)
}

// GitHub's payloads are capped at 25 MB; push payloads are far smaller.
const maxBody = 25 << 20

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/github" {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	if !validSignature(h.secret, body, r.Header.Get("X-Hub-Signature-256")) {
		log.Printf("apply-hook: rejected a delivery with a bad signature from %s", r.RemoteAddr)
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}
	event, delivery := r.Header.Get("X-GitHub-Event"), r.Header.Get("X-GitHub-Delivery")
	if event != "ping" {
		h.fire(event + " " + delivery)
	}
	w.WriteHeader(http.StatusAccepted)
}

func validSignature(secret, body []byte, header string) bool {
	got, err := hex.DecodeString(strings.TrimPrefix(header, "sha256="))
	if err != nil || !strings.HasPrefix(header, "sha256=") {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

// trigger starts the apply unit, and if it's already running, starts it once
// more when it finishes, so a push during an apply isn't left for the timer.
// Any number of pings during one apply make one more run.
type trigger struct {
	unit    string
	mu      sync.Mutex
	pending bool
	waiting bool
	// overridable in tests
	active func(unit string) bool
	start  func(unit string) error
}

func (t *trigger) isActive() bool {
	if t.active != nil {
		return t.active(t.unit)
	}
	out, _ := exec.Command("systemctl", "is-active", t.unit).Output()
	s := strings.TrimSpace(string(out))
	return s == "active" || s == "activating" || s == "deactivating" || s == "reloading"
}

func (t *trigger) startUnit() error {
	if t.start != nil {
		return t.start(t.unit)
	}
	return exec.Command("systemctl", "start", "--no-block", t.unit).Run()
}

func (t *trigger) fire(reason string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.isActive() {
		log.Printf("apply-hook: %s: starting %s", reason, t.unit)
		if err := t.startUnit(); err != nil {
			log.Printf("apply-hook: start %s: %v", t.unit, err)
		}
		return
	}
	t.pending = true
	if t.waiting {
		return
	}
	t.waiting = true
	log.Printf("apply-hook: %s: %s is running; will run it again when it finishes", reason, t.unit)
	go t.afterCurrent()
}

func (t *trigger) afterCurrent() {
	for t.isActive() {
		time.Sleep(time.Second)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.waiting = false
	if t.pending {
		t.pending = false
		if err := t.startUnit(); err != nil {
			log.Printf("apply-hook: start %s: %v", t.unit, err)
		}
	}
}

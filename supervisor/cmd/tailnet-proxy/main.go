// tailnet-proxy puts one web page from Eldar's VM on Ben's tailnet: it joins
// as its own node (tsnet) and reverse-proxies HTTPS (WebSockets included) to
// a URL only the host can reach, such as the stream of the VM's Chrome. Only
// untagged devices of the allowed tailnet user get through; the VM itself
// never touches the tailnet.
//
// Configured by environment: PROXY_HOSTNAME (tailnet name), PROXY_STATE_DIR
// (tsnet state; a one-time auth key in authkey there), PROXY_TARGET (e.g.
// http://10.42.0.2:3000), PROXY_ALLOWED_USER (tailnet login), and optionally
// PROXY_FRAME_HOSTS: tailnet machines whose HTTPS origin may frame this page
// (e.g. the supervisor web UI's Browser tab); no one else may.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/tsnet"
)

func main() {
	hostname, stateDir := os.Getenv("PROXY_HOSTNAME"), os.Getenv("PROXY_STATE_DIR")
	allowed := os.Getenv("PROXY_ALLOWED_USER")
	if allowed == "" {
		allowed = os.Getenv("WEB_ALLOWED_USER") // the supervisor web UI's setting
	}
	target, err := url.Parse(os.Getenv("PROXY_TARGET"))
	if hostname == "" || stateDir == "" || allowed == "" || err != nil || target.Host == "" {
		log.Fatal("tailnet-proxy: set PROXY_HOSTNAME, PROXY_STATE_DIR, PROXY_TARGET and PROXY_ALLOWED_USER")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ts := &tsnet.Server{Hostname: hostname, Dir: stateDir, Logf: func(string, ...any) {}, UserLogf: log.Printf}
	keyFile := filepath.Join(stateDir, "authkey")
	if b, err := os.ReadFile(keyFile); err == nil {
		ts.AuthKey = strings.TrimSpace(string(b))
	}
	defer ts.Close()
	if _, err := ts.Up(ctx); err != nil {
		log.Fatalf("tailnet-proxy: tailnet: %v", err)
	}
	if ts.AuthKey != "" {
		os.Remove(keyFile) // registered; the one-time key has no further use
	}
	lc, err := ts.LocalClient()
	if err != nil {
		log.Fatal(err)
	}
	ln, err := ts.ListenTLS("tcp", ":443")
	if err != nil {
		log.Fatalf("tailnet-proxy: %v", err)
	}
	if d := ts.CertDomains(); len(d) > 0 {
		log.Printf("tailnet-proxy: https://%s → %s (for %s)", d[0], target, allowed)
	}
	rp := httputil.NewSingleHostReverseProxy(target)
	// Framing: only by the listed tailnet machines' HTTPS origins.
	ancestors := "'none'"
	if d := ts.CertDomains(); len(d) > 0 {
		if _, suffix, ok := strings.Cut(d[0], "."); ok {
			var origins []string
			for _, h := range strings.Fields(strings.ReplaceAll(os.Getenv("PROXY_FRAME_HOSTS"), ",", " ")) {
				origins = append(origins, "https://"+h+"."+suffix)
			}
			if len(origins) > 0 {
				ancestors = strings.Join(origins, " ")
			}
		}
	}
	rp.ModifyResponse = func(res *http.Response) error {
		res.Header.Del("X-Frame-Options")
		res.Header.Set("Content-Security-Policy", "frame-ancestors "+ancestors)
		return nil
	}
	srv := &http.Server{Handler: admit(lc, allowed, rp), ReadHeaderTimeout: 10 * time.Second}
	go func() { <-ctx.Done(); srv.Close() }()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

// admit lets through only untagged devices owned by the allowed user.
func admit(lc *local.Client, allowed string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		who, err := lc.WhoIs(r.Context(), r.RemoteAddr)
		if err != nil || who.Node == nil || who.UserProfile == nil || len(who.Node.Tags) > 0 ||
			!strings.EqualFold(who.UserProfile.LoginName, allowed) {
			http.Error(w, "not allowed", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

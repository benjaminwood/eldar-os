package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/benjaminwood/eldar-os/supervisor/api"
)

// Devices are physical controls, such as a Stream Deck on Ben's Mac, that act
// through the supervisor (server/devices.go). They get the same tailnet check
// as the UI (one of the allowed user's untagged machines) but no passkey: the
// device's own token stands in, and the supervisor limits what it can do. A
// device whose browser an agent drives isn't refused here, as the UI is; the
// supervisor turns its Approve off instead, and Deny stays useful.
//
//	POST /device/pair              {name} → {device, token}, once Ben approves
//	GET  /device/events            what to show, as server-sent events ("view")
//	POST /device/actions/{action}  {ref} a press: approve | deny
//
// The token goes in "Authorization: Bearer …"; writes also need X-Supervisor: 1.
func (s *server) deviceRoutes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /device/pair", s.devicePair)
	mux.HandleFunc("GET /device/events", s.deviceEvents)
	mux.HandleFunc("POST /device/actions/{action}", s.deviceAction)
	return mux
}

type deviceKey struct{}

// deviceWhois admits the allowed user's untagged machines and records which
// one is calling, for the supervisor to check the token against.
func (s *server) deviceWhois(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		who, err := s.lc.WhoIs(r.Context(), r.RemoteAddr)
		if err != nil || who.Node == nil || who.UserProfile == nil {
			http.Error(w, "unknown peer", http.StatusForbidden)
			return
		}
		if len(who.Node.Tags) > 0 || !strings.EqualFold(who.UserProfile.LoginName, s.cfg.AllowedUser) {
			log.Printf("device: refused %s (%s, tags %v)", who.Node.ComputedName, who.UserProfile.LoginName, who.Node.Tags)
			http.Error(w, "not allowed", http.StatusForbidden)
			return
		}
		c := api.DeviceCaller{Node: who.Node.Name, BrowserDriven: s.browserDriven(r.Context(), who.Node.Name)}
		for _, a := range who.Node.Addresses {
			c.IPs = append(c.IPs, a.Addr().String())
		}
		ctx := context.WithValue(r.Context(), deviceKey{}, c)
		ctx = api.WithClientName(ctx, "device · "+who.Node.ComputedName)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// caller is the device behind r: its machine (from deviceWhois; "dev" on the
// loopback development listener) and its token.
func caller(r *http.Request) api.DeviceCaller {
	c, ok := r.Context().Value(deviceKey{}).(api.DeviceCaller)
	if !ok {
		c = api.DeviceCaller{Node: "dev"}
	}
	c.Token, _ = strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return c
}

func (s *server) devicePair(w http.ResponseWriter, r *http.Request) {
	b, ok := readBody[struct {
		Name string `json:"name"`
	}](w, r)
	if !ok {
		return
	}
	// Blocks until Ben answers (or the approval times out).
	dev, token, err := s.cfg.Client.PairDevice(r.Context(), b.Name, caller(r))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"device": dev, "token": token})
}

func (s *server) deviceAction(w http.ResponseWriter, r *http.Request) {
	b, ok := readBody[struct {
		Ref int `json:"ref"`
	}](w, r)
	if !ok {
		return
	}
	ok2(w, s.cfg.Client.DeviceAction(r.Context(), caller(r), r.PathValue("action"), b.Ref))
}

// deviceEvents sends the device's view on connect and whenever the
// supervisor's state changes (at least every 5 seconds, as a heartbeat).
// "unpaired" means the token is no longer valid; "down" that the supervisor
// can't be reached for now.
func (s *server) deviceEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	c := caller(r)
	if _, err := s.cfg.Client.DeviceView(r.Context(), c); err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(200)
	fl.Flush()
	for ev := range s.cfg.Client.Subscribe(r.Context()) {
		frame := `event: down` + "\ndata: {}\n\n"
		if ev.State != nil {
			v, err := s.cfg.Client.DeviceView(r.Context(), c)
			var ae *api.APIError
			switch {
			case err == nil:
				b, _ := json.Marshal(v)
				frame = "event: view\ndata: " + string(b) + "\n\n"
			case errors.As(err, &ae) && ae.Status == 401:
				io.WriteString(w, "event: unpaired\ndata: {}\n\n")
				fl.Flush()
				return
			}
		}
		if _, err := io.WriteString(w, frame); err != nil {
			return
		}
		fl.Flush()
	}
}

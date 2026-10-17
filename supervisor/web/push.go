package web

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"

	"github.com/benjaminwood/eldar-os/supervisor/api"
	"github.com/benjaminwood/eldar-os/supervisor/view"
)

// pusher sends a Web Push notification to every subscribed device whenever a
// new approval appears. VAPID keys and subscriptions live in the state dir.
type pusher struct {
	dir    string
	client *api.Client
	origin string // https://<host>, the VAPID subscriber

	mu   sync.Mutex
	keys struct{ Public, Private string }
	subs []subscription
}

type subscription struct {
	webpush.Subscription
	Device  string    `json:"device"`
	Created time.Time `json:"created"`
}

func newPusher(dir string, client *api.Client, host string) (*pusher, error) {
	p := &pusher{dir: dir, client: client, origin: "https://" + host}
	kp := filepath.Join(dir, "vapid.json")
	if b, err := os.ReadFile(kp); err == nil {
		if err := json.Unmarshal(b, &p.keys); err != nil {
			return nil, err
		}
	} else {
		priv, pub, err := webpush.GenerateVAPIDKeys()
		if err != nil {
			return nil, err
		}
		p.keys.Public, p.keys.Private = pub, priv
		if err := writeFile0600(kp, p.keys); err != nil {
			return nil, err
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "push-subscriptions.json")); err == nil {
		_ = json.Unmarshal(b, &p.subs)
	}
	return p, nil
}

func writeFile0600(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (p *pusher) saveLocked() {
	if err := writeFile0600(filepath.Join(p.dir, "push-subscriptions.json"), p.subs); err != nil {
		log.Printf("push: saving subscriptions: %v", err)
	}
}

type notice struct {
	Title   string `json:"title"`
	Body    string `json:"body"`
	Tag     string `json:"tag"`
	ID      int    `json:"id,omitempty"`
	URL     string `json:"url,omitempty"` // where tapping the notification opens
	Kind    string `json:"kind,omitempty"`
	Actions bool   `json:"actions"`
}

// send delivers to every device, dropping subscriptions the push service says
// are gone.
func (p *pusher) send(ctx context.Context, n notice) {
	payload, _ := json.Marshal(n)
	p.mu.Lock()
	subs := append([]subscription(nil), p.subs...)
	p.mu.Unlock()

	var dead []string
	for _, s := range subs {
		sub := s.Subscription
		res, err := webpush.SendNotificationWithContext(ctx, payload, &sub, &webpush.Options{
			Subscriber:      p.origin,
			VAPIDPublicKey:  p.keys.Public,
			VAPIDPrivateKey: p.keys.Private,
			TTL:             180,
			Urgency:         webpush.UrgencyHigh,
			Topic:           pushTopic(n.Tag),
		})
		if err != nil {
			log.Printf("push to %s: %v", s.Device, err)
			continue
		}
		// The push service says why it refused in the body (Apple: {"reason": …}).
		why, _ := io.ReadAll(io.LimitReader(res.Body, 300))
		res.Body.Close()
		switch res.StatusCode {
		case http.StatusGone, http.StatusNotFound:
			dead = append(dead, s.Endpoint)
		default:
			if res.StatusCode >= 300 {
				log.Printf("push to %s: %s %s", s.Device, res.Status, strings.TrimSpace(string(why)))
			}
		}
	}
	if len(dead) > 0 {
		p.mu.Lock()
		keep := p.subs[:0]
		for _, s := range p.subs {
			if !contains(dead, s.Endpoint) {
				keep = append(keep, s)
			}
		}
		p.subs = keep
		p.saveLocked()
		p.mu.Unlock()
	}
}

// pushTopic turns a notice tag into a Web Push Topic: a newer push with the
// same topic replaces an undelivered older one. Apple decodes the topic as
// base64url (at most 32 characters) and rejects any string that isn't valid
// base64url with BadWebPushTopic, so hash the tag and encode it to 32 characters.
func pushTopic(tag string) string {
	sum := sha256.Sum256([]byte(tag))
	return base64.RawURLEncoding.EncodeToString(sum[:])[:32]
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// watch follows the admin stream and notifies on each new pending approval.
// Approvals already waiting when the watcher (re)connects are not re-sent.
func (p *pusher) watch(ctx context.Context) {
	seen := map[int]bool{}
	seenSignIn := map[string]bool{}
	seenChat := 0
	primed := false
	for ev := range p.client.Subscribe(ctx) {
		if ev.State == nil {
			continue
		}
		st := *ev.State
		sessions := view.Sessions(st)
		send := primed && st.Settings.MobilePush
		for _, pd := range st.Pending {
			if seen[pd.ID] {
				continue
			}
			seen[pd.ID] = true
			if !send {
				continue
			}
			go p.send(ctx, pendingNotice(pd, sessions))
		}
		for _, si := range st.MCP.Pending {
			key := si.Name + "@" + si.StartedAt
			if seenSignIn[key] {
				continue
			}
			seenSignIn[key] = true
			if send {
				go p.send(ctx, notice{
					Title: "Sign in to " + si.Name,
					Body:  "An MCP server is waiting for you to authorize it.\n" + si.ServerURL,
					Tag:   "mcp-" + si.Name,
					URL:   "/#settings",
				})
			}
		}
		if c := st.Chat; c.LastID > seenChat {
			if send && c.Last != nil && c.Last.From == "eldar" && c.Last.ID > seenChat {
				go p.send(ctx, chatNotice(*c.Last, c.LastID-seenChat))
			}
			seenChat = c.LastID
		}
		primed = true
	}
}

// chatNotice is a push for a message from Eldar. n > 1 when several arrived
// between snapshots; the notice shows the newest.
func chatNotice(m api.ChatMessage, n int) notice {
	body := m.Text
	if r := []rune(body); len(r) > 280 {
		body = string(r[:279]) + "…"
	}
	title := "Eldar"
	if n > 1 {
		title = fmt.Sprintf("Eldar (%d new)", n)
	}
	return notice{Title: title, Body: body, Tag: fmt.Sprintf("chat-%d", m.ID), URL: "/#chat"}
}

func pendingNotice(pd api.Pending, sessions []view.Session) notice {
	who := view.SessionTitle(sessions, pd.SessionID)
	body := strings.SplitN(pd.Title, "\n", 2)[0]
	if len(body) > 140 {
		body = body[:139] + "…"
	}
	meta := who
	if pd.Profile != "" {
		meta += " · " + pd.Profile
	}
	if c := view.CredsOf(pd); c != "" {
		meta += " · " + c
	}
	if pd.AwsProfile != "" {
		meta += " · AWS " + view.AwsLabel(pd.AwsProfile)
		if view.AwsRisky(pd.AwsProfile) {
			meta = "⚠ " + meta
		}
	}
	if pd.ArcProfileName != "" {
		meta += " · Arc " + pd.ArcProfileName
	}
	return notice{
		Title:   fmt.Sprintf("Approve #%d? %s", pd.ID, strings.ToLower(view.KindLabelFor(pd.Kind, pd.Target))),
		Body:    body + "\n" + meta,
		Tag:     fmt.Sprintf("pending-%d", pd.ID),
		ID:      pd.ID,
		Kind:    pd.Kind,
		Actions: true,
	}
}

func (s *server) pushKey(w http.ResponseWriter, r *http.Request) {
	s.push.mu.Lock()
	n := len(s.push.subs)
	s.push.mu.Unlock()
	writeJSON(w, 200, map[string]any{"key": s.push.keys.Public, "devices": n})
}

func (s *server) pushSubscribe(w http.ResponseWriter, r *http.Request) {
	sub, ok := readBody[webpush.Subscription](w, r)
	if !ok {
		return
	}
	if !strings.HasPrefix(sub.Endpoint, "https://") || sub.Keys.P256dh == "" || sub.Keys.Auth == "" {
		writeJSON(w, 400, map[string]string{"error": "invalid subscription"})
		return
	}
	device, _ := r.Context().Value(ctxKey("device")).(string)
	p := s.push
	p.mu.Lock()
	for i, x := range p.subs {
		if x.Endpoint == sub.Endpoint {
			p.subs = append(p.subs[:i], p.subs[i+1:]...)
			break
		}
	}
	p.subs = append(p.subs, subscription{Subscription: sub, Device: device, Created: time.Now()})
	p.saveLocked()
	p.mu.Unlock()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *server) pushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	b, ok := readBody[struct {
		Endpoint string `json:"endpoint"`
	}](w, r)
	if !ok {
		return
	}
	p := s.push
	p.mu.Lock()
	keep := p.subs[:0]
	for _, x := range p.subs {
		if x.Endpoint != b.Endpoint {
			keep = append(keep, x)
		}
	}
	p.subs = keep
	p.saveLocked()
	p.mu.Unlock()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *server) pushTest(w http.ResponseWriter, r *http.Request) {
	go s.push.send(context.Background(), notice{
		Title: "✦ Eldar notifications are on",
		Body:  "You'll get one of these the moment an approval is waiting.",
		Tag:   "test",
	})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

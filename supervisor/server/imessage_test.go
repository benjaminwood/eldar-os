package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/benjaminwood/eldar-os/supervisor/api"
)

// fakeBlueBubbles serves the few API calls the bridge makes.
type fakeBlueBubbles struct {
	mu   sync.Mutex
	msgs []map[string]any // the Mac's message history, oldest first
	sent []map[string]any
}

func (f *fakeBlueBubbles) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("password") != "pw" {
			w.WriteHeader(401)
			json.NewEncoder(w).Encode(map[string]any{"status": 401, "message": "You are not authorized"})
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		var data any
		switch {
		case r.URL.Path == "/api/v1/message/query" && body["sort"] == "DESC":
			data = f.msgs[len(f.msgs)-1:]
		case r.URL.Path == "/api/v1/message/query":
			where := body["where"].([]any)[0].(map[string]any)
			after := where["args"].(map[string]any)["rowid"].(float64)
			out := []map[string]any{}
			for _, m := range f.msgs {
				if m["originalROWID"].(int) > int(after) {
					out = append(out, m)
				}
			}
			data = out
		case r.URL.Path == "/api/v1/message/text":
			f.sent = append(f.sent, body)
			data = map[string]any{"guid": "sent"}
		default:
			t.Errorf("unexpected BlueBubbles call %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"status": 200, "message": "Success", "data": data})
	})
}

func bbMsg(rowid int, from, text string, mods ...func(map[string]any)) map[string]any {
	m := map[string]any{"originalROWID": rowid, "guid": "g" + from + text, "text": text, "isFromMe": false,
		"dateCreated": 1700000000000 + rowid, "associatedMessageType": nil,
		"handle": map[string]any{"address": from},
		"chats":  []map[string]any{{"guid": "iMessage;-;" + from, "style": 45}}}
	for _, f := range mods {
		f(m)
	}
	return m
}

// newIMessageTest: a supervisor bridged to a fake BlueBubbles, with session
// "s1" as the Claude session in workspace primary, tab Family.
func newIMessageTest(t *testing.T, bb *fakeBlueBubbles) *Supervisor {
	t.Helper()
	s, _ := newTest(t)
	srv := httptest.NewServer(bb.handler(t))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	cfg := "server: " + srv.URL + "\nsession: { workspace: primary, tab: Family }\ncontacts:\n  - { name: Ben, address: \"(555) 555-0100\" }\n"
	os.WriteFile(filepath.Join(dir, "imessage.yaml"), []byte(cfg), 0o600)
	os.WriteFile(filepath.Join(dir, "pw"), []byte("pw\n"), 0o600)
	s.imessage = NewIMessage(filepath.Join(dir, "imessage.yaml"), filepath.Join(dir, "pw"),
		filepath.Join(dir, "imessage.jsonl"), filepath.Join(dir, "imessage-state.json"))
	s.herdr.cache[s.herdr.DefaultSession] = herdrCached{at: time.Now().Add(time.Hour), view: api.HerdrView{OK: true, Spaces: []api.HerdrSpace{
		{Label: "primary", Tabs: []api.HerdrTab{{Label: "Family", Panes: []api.HerdrPaneView{
			{PaneID: "p1", Agent: &api.HerdrAgent{Kind: "claude", SessionID: "s1"}}}}}}}}}
	return s
}

func TestNormAddress(t *testing.T) {
	for in, want := range map[string]string{"(555) 555-0100": "+15555550100", "15555550100": "+15555550100",
		"+1 555 555 0100": "+15555550100", "Someone@iCloud.com ": "someone@icloud.com", "+44 20 7946 0000": "+442079460000"} {
		if got := normAddress(in); got != want {
			t.Errorf("normAddress(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIMessagePollDeliversOnlyAllowedDirectMessages(t *testing.T) {
	bb := &fakeBlueBubbles{msgs: []map[string]any{bbMsg(10, "+15555550100", "old history")}}
	s := newIMessageTest(t, bb)
	ctx := context.Background()
	if err := s.pollIMessage(ctx); err != nil { // the first poll only finds where history ends
		t.Fatal(err)
	}
	if len(s.imessage.events) != 0 || s.imessage.state.LastRowID != 10 {
		t.Fatalf("baseline: %d events, last %d", len(s.imessage.events), s.imessage.state.LastRowID)
	}
	bb.mu.Lock()
	bb.msgs = append(bb.msgs,
		bbMsg(11, "+15555550100", "hello"),
		bbMsg(12, "+19999999999", "stranger danger"),
		bbMsg(13, "+15555550100", "in a group", func(m map[string]any) {
			m["chats"] = []map[string]any{{"guid": "iMessage;+;chat1", "style": 43}}
		}),
		bbMsg(14, "+15555550100", "sent by me", func(m map[string]any) { m["isFromMe"] = true }),
		bbMsg(15, "+15555550100", "", func(m map[string]any) { m["associatedMessageType"] = 2000 }),
	)
	bb.mu.Unlock()
	if err := s.pollIMessage(ctx); err != nil {
		t.Fatal(err)
	}
	ev := s.imessage.events
	if len(ev) != 1 || ev[0].Text != "hello" || ev[0].Name != "Ben" || ev[0].From != "+15555550100" {
		t.Fatalf("events %+v", ev)
	}
	if s.imessage.state.LastRowID != 15 {
		t.Fatalf("last row %d", s.imessage.state.LastRowID)
	}
	audit, _ := os.ReadFile(s.cfg.AuditPath)
	if strings.Contains(string(audit), "stranger danger") || !strings.Contains(string(audit), "imessage from +19999999999 ignored") {
		t.Fatalf("audit should name the ignored sender, never their text:\n%s", audit)
	}
	again := NewIMessage(s.imessage.path, s.imessage.passwordFile, s.imessage.eventsPath, s.imessage.statePath)
	if len(again.events) != 1 || again.state.LastRowID != 15 || again.chats["+15555550100"] != "iMessage;-;+15555550100" {
		t.Fatalf("reload: %+v", again.state)
	}
}

func TestIMessageSendOnlyToAllowedFromTheSession(t *testing.T) {
	bb := &fakeBlueBubbles{}
	s := newIMessageTest(t, bb)
	h := s.agentHandler()
	send := func(session, key, to string) int {
		req := httptest.NewRequest("POST", "/imessage/send", strings.NewReader(`{"to":"`+to+`","text":"hi there"}`))
		req.Header.Set("X-Session-Id", session)
		req.Header.Set("X-Chat-Key", key)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if c := send("s1", "k", "555-555-0100"); c != 200 {
		t.Fatalf("allowed contact: %d", c)
	}
	if len(bb.sent) != 1 || bb.sent[0]["chatGuid"] != "iMessage;-;+15555550100" || bb.sent[0]["message"] != "hi there" {
		t.Fatalf("sent %+v", bb.sent)
	}
	if c := send("s1", "k", "+19999999999"); c != 403 {
		t.Fatalf("stranger: %d", c)
	}
	if c := send("other", "k", "+15555550100"); c != 403 {
		t.Fatalf("another session: %d", c)
	}
	if c := send("s1", "different-key", "+15555550100"); c != 403 {
		t.Fatalf("another key: %d", c)
	}
	if len(bb.sent) != 1 {
		t.Fatalf("refused sends reached BlueBubbles: %+v", bb.sent)
	}
}

func TestIMessageErrorsHideThePassword(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "pw"), []byte("secret-pw"), 0o600)
	m := NewIMessage("", filepath.Join(dir, "pw"), filepath.Join(dir, "e"), filepath.Join(dir, "st"))
	_, err := m.call(context.Background(), imessageFile{Server: "http://127.0.0.1:1"}, "GET", "ping", nil, nil)
	if err == nil || strings.Contains(err.Error(), "secret-pw") {
		t.Fatalf("error %v", err)
	}
}

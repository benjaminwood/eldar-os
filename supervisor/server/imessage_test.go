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
	mu    sync.Mutex
	msgs  []map[string]any // the Mac's message history, oldest first
	sent  []map[string]any
	chats map[string]map[string]any // chat GUID -> chat (with participants)
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
		case strings.HasPrefix(r.URL.Path, "/api/v1/chat/"):
			c, ok := f.chats[strings.TrimPrefix(r.URL.Path, "/api/v1/chat/")]
			if !ok {
				w.WriteHeader(404)
				json.NewEncoder(w).Encode(map[string]any{"status": 404, "message": "Chat does not exist!"})
				return
			}
			data = c
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
	cfg := "server: " + srv.URL + "\nsession: { workspace: primary, tab: Family }\ncontacts:\n  - { name: Ben, address: \"(555) 555-0100\" }\n  - { name: Sam, address: \"+15555550111\", direct: false }\n"
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

// group puts a message in group chat guid.
func group(guid string) func(map[string]any) {
	return func(m map[string]any) { m["chats"] = []map[string]any{{"guid": guid, "style": 43}} }
}

func bbChat(guid, name string, members ...string) map[string]any {
	var ps []map[string]any
	for _, a := range members {
		ps = append(ps, map[string]any{"address": a})
	}
	return map[string]any{"guid": guid, "style": 43, "displayName": name, "participants": ps}
}

func TestIMessagePollDeliversOnlyAllowedChats(t *testing.T) {
	bb := &fakeBlueBubbles{msgs: []map[string]any{bbMsg(10, "+15555550100", "old history")}, chats: map[string]map[string]any{
		"iMessage;+;fam":     bbChat("iMessage;+;fam", "Us", "+15555550100", "+15555550111"),
		"iMessage;+;mixed":   bbChat("iMessage;+;mixed", "", "+15555550100", "+19999999999"),
		"iMessage;+;samonly": bbChat("iMessage;+;samonly", "", "+15555550111"),
	}}
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
		bbMsg(13, "+15555550100", "group with a stranger", group("iMessage;+;mixed")),
		bbMsg(14, "+15555550100", "sent by me", func(m map[string]any) { m["isFromMe"] = true }),
		bbMsg(15, "+15555550100", "", func(m map[string]any) { m["associatedMessageType"] = 2000 }),
		bbMsg(16, "+15555550111", "sam directly"),
		bbMsg(17, "+15555550111", "sam alone in a group", group("iMessage;+;samonly")),
		bbMsg(18, "+15555550111", "sam in the family group", group("iMessage;+;fam")),
	)
	bb.mu.Unlock()
	if err := s.pollIMessage(ctx); err != nil {
		t.Fatal(err)
	}
	ev := s.imessage.events
	if len(ev) != 2 || ev[0].Text != "hello" || ev[0].Name != "Ben" || ev[0].From != "+15555550100" || ev[0].Group {
		t.Fatalf("events %+v", ev)
	}
	if g := ev[1]; g.Text != "sam in the family group" || !g.Group || g.Chat != "iMessage;+;fam" || g.ChatName != "Us" ||
		strings.Join(g.Members, ",") != "Ben,Sam" || g.Name != "Sam" {
		t.Fatalf("group event %+v", g)
	}
	if s.imessage.state.LastRowID != 18 {
		t.Fatalf("last row %d", s.imessage.state.LastRowID)
	}
	audit, _ := os.ReadFile(s.cfg.AuditPath)
	if strings.Contains(string(audit), "stranger danger") || !strings.Contains(string(audit), "imessage from +19999999999 ignored") {
		t.Fatalf("audit should name the ignored sender, never their text:\n%s", audit)
	}
	again := NewIMessage(s.imessage.path, s.imessage.passwordFile, s.imessage.eventsPath, s.imessage.statePath)
	if len(again.events) != 2 || again.state.LastRowID != 18 || again.chats["+15555550100"] != "iMessage;-;+15555550100" ||
		again.chats["+15555550111"] != "" {
		t.Fatalf("reload: %+v", again.state)
	}
}

func TestIMessageStartsOverWhenTheDatabaseIsReplaced(t *testing.T) {
	bb := &fakeBlueBubbles{msgs: []map[string]any{bbMsg(500, "+15555550100", "on the old Mac")}}
	s := newIMessageTest(t, bb)
	ctx := context.Background()
	if err := s.pollIMessage(ctx); err != nil || s.imessage.state.LastRowID != 500 {
		t.Fatalf("baseline: %v, last %d", err, s.imessage.state.LastRowID)
	}
	bb.mu.Lock() // BlueBubbles moves to another Mac, whose history numbers from 1
	bb.msgs = []map[string]any{bbMsg(3, "+15555550100", "synced from iCloud")}
	bb.mu.Unlock()
	if err := s.pollIMessage(ctx); err != nil || s.imessage.state.LastRowID != 3 || len(s.imessage.events) != 0 {
		t.Fatalf("replaced: %v, last %d, %d events", err, s.imessage.state.LastRowID, len(s.imessage.events))
	}
	bb.mu.Lock()
	bb.msgs = append(bb.msgs, bbMsg(4, "+15555550100", "hello from the new Mac"))
	bb.mu.Unlock()
	if err := s.pollIMessage(ctx); err != nil {
		t.Fatal(err)
	}
	if ev := s.imessage.events; len(ev) != 1 || ev[0].Text != "hello from the new Mac" {
		t.Fatalf("events %+v", ev)
	}
}

func TestIMessageSendOnlyToAllowedFromTheSession(t *testing.T) {
	bb := &fakeBlueBubbles{chats: map[string]map[string]any{
		"iMessage;+;fam":   bbChat("iMessage;+;fam", "", "+15555550100", "+15555550111"),
		"iMessage;+;mixed": bbChat("iMessage;+;mixed", "", "+15555550100", "+19999999999"),
	}}
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
	if c := send("s1", "k", "+15555550111"); c != 403 {
		t.Fatalf("a group-only contact, directly: %d", c)
	}
	if c := send("s1", "k", "iMessage;+;mixed"); c != 403 {
		t.Fatalf("a group with a stranger: %d", c)
	}
	if c := send("s1", "k", "iMessage;+;fam"); c != 200 {
		t.Fatalf("the family group: %d", c)
	}
	if c := send("other", "k", "+15555550100"); c != 403 {
		t.Fatalf("another session: %d", c)
	}
	if c := send("s1", "different-key", "+15555550100"); c != 403 {
		t.Fatalf("another key: %d", c)
	}
	if len(bb.sent) != 2 || bb.sent[1]["chatGuid"] != "iMessage;+;fam" {
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

func TestIMessageWebhookOnlyNudgesAPoll(t *testing.T) {
	bb := &fakeBlueBubbles{msgs: []map[string]any{bbMsg(10, "+15555550100", "old history")}}
	s := newIMessageTest(t, bb)
	s.cfg.IMessageWebhook = ":0"
	s.pollIMessage(context.Background()) // baseline
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.runIMessage(ctx) // polls once now, then every imessageBackup
	time.Sleep(100 * time.Millisecond)
	bb.mu.Lock()
	bb.msgs = append(bb.msgs, bbMsg(11, "+15555550100", "hello"))
	bb.mu.Unlock()
	hook := s.imessageWebhook()
	// The body is ignored: a forged message in it never becomes an event.
	forged := `{"type":"new-message","data":{"text":"forged","handle":{"address":"+15555550100"}}}`
	for range 3 { // nudges coalesce; none blocks
		rec := httptest.NewRecorder()
		hook.ServeHTTP(rec, httptest.NewRequest("POST", "/bluebubbles/webhook", strings.NewReader(forged)))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("webhook: %d", rec.Code)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		s.imessage.mu.Lock()
		n := len(s.imessage.events)
		s.imessage.mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.imessage.mu.Lock()
	defer s.imessage.mu.Unlock()
	if len(s.imessage.events) != 1 || s.imessage.events[0].Text != "hello" {
		t.Fatalf("events %+v", s.imessage.events)
	}
}

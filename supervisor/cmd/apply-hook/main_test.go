package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func sign(secret, body string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

func TestHandlerChecksSignatureAndIgnoresPayload(t *testing.T) {
	var fired []string
	h := &handler{secret: []byte("s3cret"), fire: func(r string) { fired = append(fired, r) }}
	post := func(path, event, body, sig string) int {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("X-GitHub-Event", event)
		req.Header.Set("X-Hub-Signature-256", sig)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code
	}
	body := `{"ref":"refs/heads/anything","after":"whatever"}`
	if c := post("/github", "push", body, sign("wrong", body)); c != 401 || len(fired) != 0 {
		t.Fatalf("bad signature: %d, fired %v", c, fired)
	}
	if c := post("/github", "push", body, ""); c != 401 {
		t.Fatalf("no signature: %d", c)
	}
	if c := post("/github", "ping", body, sign("s3cret", body)); c != 202 || len(fired) != 0 {
		t.Fatalf("ping: %d, fired %v", c, fired)
	}
	if c := post("/github", "push", body, sign("s3cret", body)); c != 202 || len(fired) != 1 {
		t.Fatalf("push: %d, fired %v", c, fired)
	}
	if c := post("/other", "push", body, sign("s3cret", body)); c != 404 {
		t.Fatalf("other path: %d", c)
	}
}

func TestTriggerCoalescesWhileRunning(t *testing.T) {
	var mu sync.Mutex
	running, starts := true, 0
	tr := &trigger{unit: "u",
		active: func(string) bool { mu.Lock(); defer mu.Unlock(); return running },
		start:  func(string) error { mu.Lock(); defer mu.Unlock(); starts++; return nil },
	}
	for range 5 {
		tr.fire("push")
	}
	mu.Lock()
	if starts != 0 {
		t.Fatalf("started while running: %d", starts)
	}
	running = false
	mu.Unlock()
	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		n := starts
		mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pings during a run should start it once more after: %d starts", n)
		}
		time.Sleep(50 * time.Millisecond)
	}
	tr.fire("push") // idle now: starts right away
	mu.Lock()
	defer mu.Unlock()
	if starts != 2 {
		t.Fatalf("idle ping: %d starts", starts)
	}
}

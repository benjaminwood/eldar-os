package server

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeGitHub serves the App token, the installation's repos, one PR and a
// compare result.
func fakeGitHub(t *testing.T, pr map[string]any, compare string) *GitHubApp {
	t.Helper()
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	keyPath := filepath.Join(t.TempDir(), "app.pem")
	os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o600)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/access_tokens"):
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(map[string]any{"token": "t", "expires_at": time.Now().Add(time.Hour)})
		case r.URL.Path == "/installation/repositories":
			json.NewEncoder(w).Encode(map[string]any{"repositories": []any{map[string]any{"name": "staging", "full_name": "ben/staging"}}})
		case r.URL.Path == "/repos/ben/staging/pulls/7":
			json.NewEncoder(w).Encode(pr)
		case strings.HasPrefix(r.URL.Path, "/repos/ben/staging/compare/"):
			json.NewEncoder(w).Encode(map[string]any{"status": compare})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(api.Close)
	return &GitHubApp{AppID: "1", InstallationID: "2", KeyPath: keyPath, Repos: []string{"staging"}, APIBase: api.URL}
}

func watchTest(t *testing.T, pr map[string]any, compare string) *Supervisor {
	s, _ := newTest(t)
	dir := t.TempDir()
	s.cfg.GitHub = fakeGitHub(t, pr, compare)
	s.watches = NewWatches(filepath.Join(dir, "watches.json"))
	s.relay = NewRelay(filepath.Join(dir, "relay.jsonl"), filepath.Join(dir, "relay-state.json"))
	s.watches.Add(Watch{Repo: "ben/staging", PR: 7, SessionID: "sess", PaneID: "w1:p3"})
	return s
}

func TestWatchAppliedTellsAndRemoves(t *testing.T) {
	s := watchTest(t, map[string]any{"state": "closed", "merged": true, "merge_commit_sha": "abc", "title": "Add yq"}, "ahead")
	s.checkWatches("0123456789abcdef", true, "")
	// The session isn't in herdr here, so the notice goes to Eldar.
	if len(s.relay.events) != 1 || !strings.Contains(s.relay.events[0].Text, "PR #7 (Add yq) is merged and applied (0123456)") {
		t.Fatalf("relay: %+v", s.relay.events)
	}
	if n := len(s.watches.All()); n != 0 {
		t.Fatalf("watch should be done: %d left", n)
	}
	if again := NewWatches(s.watches.path); len(again.All()) != 0 {
		t.Fatal("removal not persisted")
	}
}

func TestWatchFailedApplyReportsOnceAndKeeps(t *testing.T) {
	s := watchTest(t, map[string]any{"state": "closed", "merged": true, "merge_commit_sha": "abc", "title": "Add yq"}, "identical")
	s.checkWatches("0123456789abcdef", false, "rolled back: health check failed")
	s.checkWatches("0123456789abcdef", false, "rolled back: health check failed")
	if len(s.relay.events) != 1 || !strings.Contains(s.relay.events[0].Text, "applying 0123456 failed: rolled back") {
		t.Fatalf("relay: %+v", s.relay.events)
	}
	if len(s.watches.All()) != 1 {
		t.Fatal("a failed apply keeps the watch for the next one")
	}
}

func TestWatchNotYetApplied(t *testing.T) {
	s := watchTest(t, map[string]any{"state": "open", "merged": false, "title": "Add yq"}, "ahead")
	s.checkWatches("0123456789abcdef", true, "")
	s2 := watchTest(t, map[string]any{"state": "closed", "merged": true, "merge_commit_sha": "abc", "title": "x"}, "behind")
	s2.checkWatches("0123456789abcdef", true, "")
	if len(s.relay.events)+len(s2.relay.events) != 0 || len(s.watches.All()) != 1 || len(s2.watches.All()) != 1 {
		t.Fatal("open PRs and merges not in the revision stay watched, silently")
	}
}

func TestWatchClosedUnmerged(t *testing.T) {
	s := watchTest(t, map[string]any{"state": "closed", "merged": false, "title": "Add yq"}, "")
	s.checkWatches("0123456789abcdef", true, "")
	if len(s.relay.events) != 1 || !strings.Contains(s.relay.events[0].Text, "closed without merging") || len(s.watches.All()) != 0 {
		t.Fatalf("relay: %+v", s.relay.events)
	}
}

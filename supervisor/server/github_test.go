package server

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
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

func TestGitHubAppToken(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	keyPath := filepath.Join(t.TempDir(), "app.pem")
	os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o600)

	calls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/app/installations/77/access_tokens" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		// The JWT is RS256 over header.claims with the App's key, issued by the App ID.
		parts := strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), ".")
		sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
		sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum[:], sig); err != nil {
			t.Errorf("bad JWT signature: %v", err)
		}
		claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
		var c map[string]any
		json.Unmarshal(claims, &c)
		if c["iss"] != "42" {
			t.Errorf("iss = %v", c["iss"])
		}
		var body struct{ Repositories []string }
		json.NewDecoder(r.Body).Decode(&body)
		if len(body.Repositories) != 1 || body.Repositories[0] != "staging" {
			t.Errorf("token not limited to the repo: %v", body.Repositories)
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"token": "ghs_test", "expires_at": time.Now().Add(time.Hour)})
	}))
	defer api.Close()

	g := &GitHubApp{AppID: "42", InstallationID: "77", KeyPath: keyPath, Repos: []string{"staging"}, APIBase: api.URL}
	tok, _, minted, err := g.Token(context.Background())
	if err != nil || tok != "ghs_test" || !minted {
		t.Fatalf("Token = %q minted=%v err=%v", tok, minted, err)
	}
	if _, _, minted, _ := g.Token(context.Background()); minted || calls != 1 {
		t.Fatalf("a fresh token should be reused (calls=%d)", calls)
	}
}

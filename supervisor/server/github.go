package server

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"
)

// GitHubApp is Eldar's identity on GitHub: an App installed on the staging
// repo that can push branches and open pull requests. A ruleset on main keeps
// it from merging or pushing there, so Ben's merge stays the approval. The
// supervisor holds the App's private key and hands out installation tokens
// (an hour each, limited to Repos); agents never see the key.
type GitHubApp struct {
	AppID, InstallationID string
	KeyPath               string
	Repos                 []string // repository names (no owner) tokens are limited to
	APIBase               string   // https://api.github.com unless testing

	http *http.Client
	mu   sync.Mutex
	tok  string
	exp  time.Time
}

func (g *GitHubApp) configured() bool {
	return g != nil && g.AppID != "" && g.InstallationID != "" && g.KeyPath != ""
}

// jwt signs the short-lived token that authenticates as the App itself.
func (g *GitHubApp) jwt(now time.Time) (string, error) {
	raw, err := os.ReadFile(g.KeyPath)
	if err != nil {
		return "", err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return "", errors.New("app key isn't PEM")
	}
	var key *rsa.PrivateKey
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		key = k
	} else if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		var ok bool
		if key, ok = k.(*rsa.PrivateKey); !ok {
			return "", errors.New("app key isn't RSA")
		}
	} else {
		return "", fmt.Errorf("app key: %w", err)
	}
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{
		"iat": now.Add(-time.Minute).Unix(), // GitHub allows for clock drift this way
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": g.AppID,
	})
	signing := header + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + enc.EncodeToString(sig), nil
}

// Token returns a valid installation token, minting a new one when the cached
// one has under ten minutes left. minted reports whether it's new.
func (g *GitHubApp) Token(ctx context.Context) (tok string, exp time.Time, minted bool, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	if g.tok != "" && now.Add(10*time.Minute).Before(g.exp) {
		return g.tok, g.exp, false, nil
	}
	jwt, err := g.jwt(now)
	if err != nil {
		return "", time.Time{}, false, err
	}
	body, _ := json.Marshal(map[string]any{"repositories": g.Repos})
	base := g.APIBase
	if base == "" {
		base = "https://api.github.com"
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/app/installations/%s/access_tokens", base, g.InstallationID), bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	hc := g.http
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	res, err := hc.Do(req)
	if err != nil {
		return "", time.Time{}, false, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusCreated {
		return "", time.Time{}, false, fmt.Errorf("GitHub answered %d: %s", res.StatusCode, truncRunes(string(raw), 200))
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Token == "" {
		return "", time.Time{}, false, fmt.Errorf("unexpected token response: %v", err)
	}
	g.tok, g.exp = out.Token, out.ExpiresAt
	return g.tok, g.exp, true, nil
}

// get calls GitHub's API as the App's installation and decodes the JSON.
func (g *GitHubApp) get(ctx context.Context, path string, out any) error {
	tok, _, _, err := g.Token(ctx)
	if err != nil {
		return err
	}
	base := g.APIBase
	if base == "" {
		base = "https://api.github.com"
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	hc := g.http
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	res, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub %s: %d %s", path, res.StatusCode, truncRunes(string(raw), 200))
	}
	return json.Unmarshal(raw, out)
}

// fullName resolves a repo name (as in Repos) to owner/name, from the
// installation's repositories.
func (g *GitHubApp) fullName(ctx context.Context, name string) (string, error) {
	var r struct {
		Repositories []struct {
			Name     string `json:"name"`
			FullName string `json:"full_name"`
		} `json:"repositories"`
	}
	if err := g.get(ctx, "/installation/repositories?per_page=100", &r); err != nil {
		return "", err
	}
	for _, x := range r.Repositories {
		if x.Name == name || x.FullName == name {
			return x.FullName, nil
		}
	}
	return "", fmt.Errorf("the App isn't installed on %s", name)
}

// handleGitHubToken: GET /github/token, for git's credential helper and gh in
// the VM. No approval: the App can only propose changes (branches, PRs), and
// Ben approves them by merging. Each new token is audited.
func (s *Supervisor) handleGitHubToken(w http.ResponseWriter, r *http.Request) {
	g := s.cfg.GitHub
	if !g.configured() {
		sendJSON(w, 503, map[string]any{"error": "No GitHub App is configured on this supervisor.", "outcome": OutBadRequest})
		return
	}
	tok, exp, minted, err := g.Token(r.Context())
	if err != nil {
		logf("github: %v", err)
		sendJSON(w, 502, map[string]any{"error": "Couldn't get a GitHub token: " + err.Error(), "outcome": OutInternal})
		return
	}
	if minted {
		s.audit.Log(AuditEntry{RequestID: s.nextID(), Action: "github app token", Decision: "allow",
			DecisionReason: fmt.Sprintf("Eldar's GitHub App, limited to %v until %s", g.Repos, exp.Format(time.RFC3339)), Mode: "normal"})
	}
	sendJSON(w, 200, map[string]any{"token": tok, "expiresAt": exp.Format(time.RFC3339)})
}

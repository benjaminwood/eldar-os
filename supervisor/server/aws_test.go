package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testAwsConfig = `[sso-session corp]
sso_start_url = https://corp.awsapps.com/start
sso_region = us-east-1
sso_registration_scopes = sso:account:access

[profile prod.ReadOnly]
sso_session = corp
sso_account_id = 111111111111
sso_role_name = ReadOnly
region = us-west-2

[profile not-sso]
region = us-east-1
`

func TestParseAwsConfig(t *testing.T) {
	cfg := parseAwsConfig(strings.NewReader(testAwsConfig))
	p, ok := cfg.profiles["prod.ReadOnly"]
	if !ok || p.AccountID != "111111111111" || p.Role != "ReadOnly" || p.Session != "corp" || p.Region != "us-west-2" {
		t.Fatalf("profile: %+v", p)
	}
	if _, ok := cfg.profiles["not-sso"]; ok {
		t.Fatal("non-SSO profiles are skipped")
	}
	if s := cfg.sessions["corp"]; s.StartURL != "https://corp.awsapps.com/start" || s.Region != "us-east-1" {
		t.Fatalf("session: %+v", s)
	}
}

func TestAwsLoginMintAndRefresh(t *testing.T) {
	var polls, refreshes, mints atomic.Int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		json.NewDecoder(r.Body).Decode(&in)
		send := func(v any) { json.NewEncoder(w).Encode(v) }
		switch r.URL.Path {
		case "/client/register":
			send(map[string]any{"clientId": "cid", "clientSecret": "csec", "clientSecretExpiresAt": time.Now().Add(90 * 24 * time.Hour).Unix()})
		case "/device_authorization":
			if in["startUrl"] != "https://corp.awsapps.com/start" {
				t.Errorf("start url %v", in["startUrl"])
			}
			send(map[string]any{"deviceCode": "dev", "userCode": "ABCD-EFGH", "verificationUriComplete": "https://device.example/?code=ABCD-EFGH", "expiresIn": 60, "interval": 1})
		case "/token":
			switch in["grantType"] {
			case "urn:ietf:params:oauth:grant-type:device_code":
				if polls.Add(1) == 1 {
					w.WriteHeader(400)
					send(map[string]any{"error": "authorization_pending"})
					return
				}
				send(map[string]any{"accessToken": "access-1", "expiresIn": 3600, "refreshToken": "refresh-1"})
			case "refresh_token":
				refreshes.Add(1)
				if in["refreshToken"] != "refresh-1" {
					t.Errorf("refresh with %v", in["refreshToken"])
				}
				send(map[string]any{"accessToken": "access-2", "expiresIn": 3600, "refreshToken": "refresh-2"})
			}
		case "/federation/credentials":
			mints.Add(1)
			if r.URL.Query().Get("account_id") != "111111111111" || r.URL.Query().Get("role_name") != "ReadOnly" || !strings.HasPrefix(r.Header.Get("x-amz-sso_bearer_token"), "access-") {
				w.WriteHeader(403)
				return
			}
			send(map[string]any{"roleCredentials": map[string]any{"accessKeyId": "AKIA", "secretAccessKey": "s", "sessionToken": "t",
				"expiration": time.Now().Add(time.Hour).UnixMilli()}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer fake.Close()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config"), []byte(testAwsConfig), 0o600)
	a := NewAWS(filepath.Join(dir, "config"), filepath.Join(dir, "aws"))
	a.oidcBase = func(string) string { return fake.URL }
	a.portalBase = func(string) string { return fake.URL }
	ctx := context.Background()

	if _, err := a.Creds(ctx, "prod.ReadOnly"); !errors.Is(err, ErrAwsMint) || !strings.Contains(err.Error(), "isn't signed in") {
		t.Fatalf("before sign-in: %v", err)
	}
	done := make(chan AwsLogin, 1)
	l, err := a.StartLogin("", func(l AwsLogin) { done <- l }) // the only session
	if err != nil || l.Session != "corp" || l.UserCode != "ABCD-EFGH" {
		t.Fatalf("start: %+v %v", l, err)
	}
	select {
	case l := <-done:
		if l.Status != "done" {
			t.Fatalf("login: %+v", l)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("login didn't finish")
	}
	if fi, _ := os.Stat(a.tokenPath("corp")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode %v", fi.Mode())
	}
	env, err := a.Creds(ctx, "prod.ReadOnly")
	if err != nil || env["AWS_ACCESS_KEY_ID"] != "AKIA" || env["AWS_REGION"] != "us-west-2" {
		t.Fatalf("creds %v %v", env, err)
	}
	a.Creds(ctx, "prod.ReadOnly")
	if mints.Load() != 1 {
		t.Fatalf("minted creds are cached: %d mints", mints.Load())
	}

	// Near expiry: refresh, and keep the rotated refresh token.
	tok, _ := a.loadToken("corp")
	tok.ExpiresAt = time.Now()
	a.saveToken("corp", tok)
	a.creds = map[string]awsCreds{}
	if _, err := a.Creds(ctx, "prod.ReadOnly"); err != nil || refreshes.Load() != 1 {
		t.Fatalf("refresh: %v (%d)", err, refreshes.Load())
	}
	if tok, _ := a.loadToken("corp"); tok.RefreshToken != "refresh-2" || tok.AccessToken != "access-2" {
		t.Fatalf("rotated token not saved: %+v", tok)
	}
	if _, err := a.Creds(ctx, "nope"); !errors.Is(err, ErrAwsMint) {
		t.Fatal("unknown profile")
	}
}

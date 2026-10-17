package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// AWS credentials for commands: a request names an AWS profile (awsProfile),
// and the supervisor mints short-lived role credentials for that one command
// through AWS IAM Identity Center (SSO), straight from its HTTP APIs.
//
// The profiles come from an ~/.aws/config-style file (private: account ids,
// role names, the SSO start URL). The supervisor signs in to each SSO session
// itself, with the device-code flow Ben approves in a browser
// (`supervisor aws-login`), and keeps that session's token in its own state
// (aws/<session>.json, mode 600): access token, rotating refresh token, and
// the client registration. Minted role credentials are cached in memory
// until a minute before they expire.

type awsProfile struct {
	Name, Session, AccountID, Role, Region string
}

type awsSession struct {
	Name, StartURL, Region string
}

type awsConfig struct {
	profiles map[string]awsProfile
	sessions map[string]awsSession
}

// parseAwsConfig reads the [profile …] and [sso-session …] sections of an
// ~/.aws/config file. Profiles without an SSO account and role are skipped.
func parseAwsConfig(r io.Reader) awsConfig {
	cfg := awsConfig{profiles: map[string]awsProfile{}, sessions: map[string]awsSession{}}
	kind, name := "", ""
	vals := map[string]string{}
	flush := func() {
		switch kind {
		case "profile":
			p := awsProfile{Name: name, Session: vals["sso_session"], AccountID: vals["sso_account_id"], Role: vals["sso_role_name"], Region: vals["region"]}
			if p.Session == "" && vals["sso_start_url"] != "" {
				// Legacy inline form: a session named by its start URL.
				p.Session = vals["sso_start_url"]
				cfg.sessions[p.Session] = awsSession{Name: p.Session, StartURL: vals["sso_start_url"], Region: vals["sso_region"]}
			}
			if p.AccountID != "" && p.Role != "" && p.Session != "" {
				cfg.profiles[name] = p
			}
		case "sso-session":
			cfg.sessions[name] = awsSession{Name: name, StartURL: vals["sso_start_url"], Region: vals["sso_region"]}
		}
		vals = map[string]string{}
	}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			flush()
			inner := strings.TrimSpace(line[1 : len(line)-1])
			switch {
			case inner == "default":
				kind, name = "profile", "default"
			case strings.HasPrefix(inner, "profile "):
				kind, name = "profile", strings.TrimSpace(strings.TrimPrefix(inner, "profile "))
			case strings.HasPrefix(inner, "sso-session "):
				kind, name = "sso-session", strings.TrimSpace(strings.TrimPrefix(inner, "sso-session "))
			default:
				kind = ""
			}
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			vals[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	flush()
	return cfg
}

// ssoToken is a signed-in SSO session (stored as aws/<session>.json).
type ssoToken struct {
	AccessToken           string    `json:"accessToken"`
	ExpiresAt             time.Time `json:"expiresAt"`
	RefreshToken          string    `json:"refreshToken,omitempty"`
	ClientID              string    `json:"clientId"`
	ClientSecret          string    `json:"clientSecret"`
	RegistrationExpiresAt time.Time `json:"registrationExpiresAt"`
	Region                string    `json:"region"`
	StartURL              string    `json:"startUrl"`
}

type awsCreds struct {
	env     map[string]string
	expires time.Time
}

// AwsLogin is a device-code sign-in in progress (or its outcome).
type AwsLogin struct {
	Session         string    `json:"session"`
	VerificationURL string    `json:"verificationUrl"`
	UserCode        string    `json:"userCode"`
	ExpiresAt       time.Time `json:"expiresAt"`
	Status          string    `json:"status"` // pending | done | failed
	Detail          string    `json:"detail,omitempty"`
}

// AWS holds the config, signed-in sessions and minted credentials.
type AWS struct {
	ConfigPath string
	StateDir   string // aws/ in the supervisor's state
	http       *http.Client
	oidcBase   func(region string) string // overridable in tests
	portalBase func(region string) string

	mu     sync.Mutex
	creds  map[string]awsCreds
	logins map[string]*AwsLogin
}

func NewAWS(configPath, stateDir string) *AWS {
	return &AWS{ConfigPath: configPath, StateDir: stateDir, http: &http.Client{Timeout: 20 * time.Second},
		oidcBase:   func(r string) string { return "https://oidc." + r + ".amazonaws.com" },
		portalBase: func(r string) string { return "https://portal.sso." + r + ".amazonaws.com" },
		creds:      map[string]awsCreds{}, logins: map[string]*AwsLogin{}}
}

func (a *AWS) config() awsConfig {
	f, err := os.Open(a.ConfigPath)
	if err != nil {
		return awsConfig{profiles: map[string]awsProfile{}, sessions: map[string]awsSession{}}
	}
	defer f.Close()
	return parseAwsConfig(f)
}

// Profiles: the AWS profiles commands may name, sorted.
func (a *AWS) Profiles() []string {
	if a == nil {
		return []string{}
	}
	names := []string{}
	for n := range a.config().profiles {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

func (a *AWS) Has(profile string) bool {
	_, ok := a.config().profiles[profile]
	return ok
}

func (a *AWS) tokenPath(session string) string {
	safe := strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' || r < ' ' {
			return '_'
		}
		return r
	}, session)
	return filepath.Join(a.StateDir, safe+".json")
}

func (a *AWS) loadToken(session string) (*ssoToken, error) {
	b, err := os.ReadFile(a.tokenPath(session))
	if err != nil {
		return nil, err
	}
	var t ssoToken
	return &t, json.Unmarshal(b, &t)
}

func (a *AWS) saveToken(session string, t *ssoToken) error {
	if err := os.MkdirAll(a.StateDir, 0o700); err != nil {
		return err
	}
	b, _ := json.Marshal(t)
	tmp := a.tokenPath(session) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, a.tokenPath(session))
}

// oidc calls an SSO OIDC API (JSON in, JSON out). An error response comes
// back as its "error" code, e.g. authorization_pending.
func (a *AWS) oidc(ctx context.Context, region, path string, in, out any) (string, error) {
	b, _ := json.Marshal(in)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, a.oidcBase(region)+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	res, err := a.http.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		var e struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		json.Unmarshal(raw, &e)
		if e.Error == "" {
			e.Error = res.Status
		}
		return e.Error, fmt.Errorf("SSO %s: %s %s", path, e.Error, e.Description)
	}
	return "", json.Unmarshal(raw, out)
}

// StartLogin begins a device-code sign-in to an SSO session and polls for
// Ben's approval in the background. The returned login has the URL and code.
func (a *AWS) StartLogin(session string, done func(AwsLogin)) (AwsLogin, error) {
	cfg := a.config()
	if session == "" && len(cfg.sessions) == 1 {
		for n := range cfg.sessions {
			session = n
		}
	}
	sess, ok := cfg.sessions[session]
	if !ok || sess.StartURL == "" || sess.Region == "" {
		var names []string
		for n := range cfg.sessions {
			names = append(names, n)
		}
		return AwsLogin{}, fmt.Errorf("no SSO session %q (have: %s)", session, strings.Join(names, ", "))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var reg struct {
		ClientID              string `json:"clientId"`
		ClientSecret          string `json:"clientSecret"`
		ClientSecretExpiresAt int64  `json:"clientSecretExpiresAt"`
	}
	if _, err := a.oidc(ctx, sess.Region, "/client/register", map[string]any{
		"clientName": "eldar-os-supervisor", "clientType": "public", "scopes": []string{"sso:account:access"}}, &reg); err != nil {
		return AwsLogin{}, err
	}
	var dev struct {
		DeviceCode              string `json:"deviceCode"`
		UserCode                string `json:"userCode"`
		VerificationURIComplete string `json:"verificationUriComplete"`
		ExpiresIn               int    `json:"expiresIn"`
		Interval                int    `json:"interval"`
	}
	if _, err := a.oidc(ctx, sess.Region, "/device_authorization", map[string]any{
		"clientId": reg.ClientID, "clientSecret": reg.ClientSecret, "startUrl": sess.StartURL}, &dev); err != nil {
		return AwsLogin{}, err
	}
	login := &AwsLogin{Session: session, VerificationURL: dev.VerificationURIComplete, UserCode: dev.UserCode,
		ExpiresAt: time.Now().Add(time.Duration(dev.ExpiresIn) * time.Second), Status: "pending"}
	a.mu.Lock()
	a.logins[session] = login
	first := *login // the poller below updates login under the lock
	a.mu.Unlock()
	go func() {
		interval := time.Duration(max(dev.Interval, 1)) * time.Second
		finish := func(status, detail string) {
			a.mu.Lock()
			login.Status, login.Detail = status, detail
			out := *login
			a.mu.Unlock()
			if done != nil {
				done(out)
			}
		}
		for time.Now().Before(login.ExpiresAt) {
			time.Sleep(interval)
			var tok struct {
				AccessToken  string `json:"accessToken"`
				ExpiresIn    int    `json:"expiresIn"`
				RefreshToken string `json:"refreshToken"`
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			code, err := a.oidc(ctx, sess.Region, "/token", map[string]any{"clientId": reg.ClientID, "clientSecret": reg.ClientSecret,
				"grantType": "urn:ietf:params:oauth:grant-type:device_code", "deviceCode": dev.DeviceCode}, &tok)
			cancel()
			switch {
			case code == "authorization_pending":
				continue
			case code == "slow_down":
				interval += 5 * time.Second
				continue
			case err != nil:
				finish("failed", err.Error())
				return
			}
			t := &ssoToken{AccessToken: tok.AccessToken, ExpiresAt: time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second),
				RefreshToken: tok.RefreshToken, ClientID: reg.ClientID, ClientSecret: reg.ClientSecret,
				RegistrationExpiresAt: time.Unix(reg.ClientSecretExpiresAt, 0), Region: sess.Region, StartURL: sess.StartURL}
			if err := a.saveToken(session, t); err != nil {
				finish("failed", "saving the token: "+err.Error())
				return
			}
			a.mu.Lock()
			a.creds = map[string]awsCreds{} // re-mint with the new sign-in
			a.mu.Unlock()
			finish("done", fmt.Sprintf("signed in; access token until %s", t.ExpiresAt.Format(time.RFC3339)))
			return
		}
		finish("failed", "the code expired before it was approved")
	}()
	return first, nil
}

func (a *AWS) Login(session string) (AwsLogin, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	l, ok := a.logins[session]
	if !ok {
		return AwsLogin{}, false
	}
	return *l, true
}

// accessToken: a valid access token for the session, refreshing it (and
// persisting the rotated refresh token) when it's near expiry.
func (a *AWS) accessToken(ctx context.Context, session string) (string, error) {
	t, err := a.loadToken(session)
	if err != nil {
		return "", fmt.Errorf("the supervisor isn't signed in to AWS SSO session %q", session)
	}
	if time.Until(t.ExpiresAt) > time.Minute {
		return t.AccessToken, nil
	}
	if t.RefreshToken == "" || time.Now().After(t.RegistrationExpiresAt) {
		return "", fmt.Errorf("the supervisor's AWS sign-in to %q has expired", session)
	}
	var tok struct {
		AccessToken  string `json:"accessToken"`
		ExpiresIn    int    `json:"expiresIn"`
		RefreshToken string `json:"refreshToken"`
	}
	if _, err := a.oidc(ctx, t.Region, "/token", map[string]any{"clientId": t.ClientID, "clientSecret": t.ClientSecret,
		"grantType": "refresh_token", "refreshToken": t.RefreshToken}, &tok); err != nil {
		return "", fmt.Errorf("the supervisor's AWS sign-in to %q has expired (refresh failed: %v)", session, err)
	}
	t.AccessToken, t.ExpiresAt = tok.AccessToken, time.Now().Add(time.Duration(tok.ExpiresIn)*time.Second)
	if tok.RefreshToken != "" {
		t.RefreshToken = tok.RefreshToken // AWS rotates it: keep the new one
	}
	if err := a.saveToken(session, t); err != nil {
		logf("aws: saving refreshed token: %v", err)
	}
	return t.AccessToken, nil
}

// ErrAwsMint wraps every failure to mint credentials.
var ErrAwsMint = errors.New("AWS credential mint failed")

// Creds returns environment variables with role credentials for a profile.
func (a *AWS) Creds(ctx context.Context, profile string) (map[string]string, error) {
	a.mu.Lock()
	if c, ok := a.creds[profile]; ok && time.Until(c.expires) > time.Minute {
		a.mu.Unlock()
		return c.env, nil
	}
	a.mu.Unlock()
	cfg := a.config()
	p, ok := cfg.profiles[profile]
	if !ok {
		return nil, fmt.Errorf("%w: unknown AWS profile %q", ErrAwsMint, profile)
	}
	sess := cfg.sessions[p.Session]
	tok, err := a.accessToken(ctx, p.Session)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAwsMint, err)
	}
	q := url.Values{"account_id": {p.AccountID}, "role_name": {p.Role}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, a.portalBase(sess.Region)+"/federation/credentials?"+q.Encode(), nil)
	req.Header.Set("x-amz-sso_bearer_token", tok)
	res, err := a.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAwsMint, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("%w: GetRoleCredentials %s: %s", ErrAwsMint, res.Status, truncRunes(string(raw), 200))
	}
	var r struct {
		RoleCredentials struct {
			AccessKeyID     string `json:"accessKeyId"`
			SecretAccessKey string `json:"secretAccessKey"`
			SessionToken    string `json:"sessionToken"`
			Expiration      int64  `json:"expiration"`
		} `json:"roleCredentials"`
	}
	if json.Unmarshal(raw, &r) != nil || r.RoleCredentials.AccessKeyID == "" {
		return nil, fmt.Errorf("%w: unexpected GetRoleCredentials response", ErrAwsMint)
	}
	env := map[string]string{"AWS_ACCESS_KEY_ID": r.RoleCredentials.AccessKeyID, "AWS_SECRET_ACCESS_KEY": r.RoleCredentials.SecretAccessKey,
		"AWS_SESSION_TOKEN": r.RoleCredentials.SessionToken}
	if p.Region != "" {
		env["AWS_DEFAULT_REGION"], env["AWS_REGION"] = p.Region, p.Region
	}
	a.mu.Lock()
	a.creds[profile] = awsCreds{env: env, expires: time.UnixMilli(r.RoleCredentials.Expiration)}
	a.mu.Unlock()
	return env, nil
}

// Status: each SSO session and whether the supervisor is signed in.
func (a *AWS) Status() []map[string]any {
	out := []map[string]any{}
	if a == nil {
		return out
	}
	cfg := a.config()
	names := []string{}
	for n := range cfg.sessions {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		st := map[string]any{"session": n, "signedIn": false}
		if t, err := a.loadToken(n); err == nil {
			st["signedIn"] = t.RefreshToken != "" && time.Now().Before(t.RegistrationExpiresAt) || time.Now().Before(t.ExpiresAt)
			st["registrationExpiresAt"] = t.RegistrationExpiresAt
		}
		if l, ok := a.Login(n); ok {
			st["login"] = l
		}
		out = append(out, st)
	}
	return out
}

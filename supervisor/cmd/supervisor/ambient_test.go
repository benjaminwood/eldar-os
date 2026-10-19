package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// fakeSupervisor answers the ambient and App token routes the client uses.
func fakeSupervisor(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/github/token":
			json.NewEncoder(w).Encode(map[string]string{"token": "app"})
		case r.URL.Path == "/ambient/git" && r.URL.Query().Get("owner") == "acme":
			json.NewEncoder(w).Encode(map[string]string{"token": "acme-pat"})
		case r.URL.Path == "/ambient/git":
			w.WriteHeader(404)
			json.NewEncoder(w).Encode(map[string]string{"error": "none"})
		case r.URL.Path == "/ambient/env":
			json.NewEncoder(w).Encode(map[string]any{"env": map[string]string{"B": "it's", "A": "1", "bad-name": "x"}})
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("SUPERVISOR_URL", srv.URL)
}

// capture runs f with stdin set to in and returns what it printed.
func capture(t *testing.T, in string, f func()) string {
	t.Helper()
	inR, inW, _ := os.Pipe()
	inW.WriteString(in)
	inW.Close()
	outR, outW, _ := os.Pipe()
	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = inR, outW
	f()
	os.Stdin, os.Stdout = oldIn, oldOut
	outW.Close()
	b, _ := io.ReadAll(outR)
	return string(b)
}

func TestGitCredentialOrder(t *testing.T) {
	fakeSupervisor(t)
	req := func(path string) string {
		return "protocol=https\nhost=github.com\npath=" + path + "\n\n"
	}
	cases := []struct{ name, githubToken, path, want string }{
		{"owner with an ambient token", "", "Acme/repo.git", "acme-pat"},
		{"owner without one: the App", "", "benjaminwood/eldar-os-staging.git", "app"},
		{"no path (useHttpPath off): the App", "", "", "app"},
		{"an approved token wins", "approved", "acme/repo.git", "approved"},
	}
	for _, c := range cases {
		t.Setenv("GITHUB_TOKEN", c.githubToken)
		out := capture(t, req(c.path), func() { runGitCredential([]string{"get"}) })
		if !strings.Contains(out, "password="+c.want+"\n") {
			t.Errorf("%s: %q", c.name, out)
		}
	}
	if out := capture(t, "protocol=https\nhost=gitlab.com\n\n", func() { runGitCredential([]string{"get"}) }); out != "" {
		t.Errorf("other host answered: %q", out)
	}
}

func TestEnvExports(t *testing.T) {
	fakeSupervisor(t)
	out := capture(t, "", func() { runEnv([]string{"/home/eldar/workspace"}) })
	if want := "export A='1'\nexport B='it'\\''s'\n"; out != want {
		t.Errorf("got %q, want %q", out, want)
	}
}

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Ambient credentials in Eldar's VM: what the supervisor hands out without
// asking (server/ambient.go, docs/decisions/0015-ambient-credentials.md).
//
//	supervisor env [DIR]   export lines for DIR (default: the current one), for
//	                       direnv: `use supervisor` in an .envrc

func supervisorURL() string {
	if u := os.Getenv("SUPERVISOR_URL"); u != "" {
		return u
	}
	return "http://10.42.0.1:9100"
}

func getJSON(path string, out any) (int, error) {
	c := &http.Client{Timeout: 30 * time.Second}
	res, err := c.Get(supervisorURL() + path)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return res.StatusCode, fmt.Errorf("supervisor answered %s", res.Status)
	}
	return res.StatusCode, nil
}

var envKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// runEnv never fails the shell that evals it: problems go to stderr and the
// directory just gets fewer (or no) variables.
func runEnv(args []string) {
	dir := ""
	if len(args) > 0 {
		dir = args[0]
	} else if wd, err := os.Getwd(); err == nil {
		dir = wd
	}
	dir, _ = filepath.Abs(dir)
	var out struct {
		Env     map[string]string
		Missing []string
		Error   string
	}
	status, err := getJSON("/ambient/env?dir="+url.QueryEscape(dir), &out)
	if err == nil && status != 200 {
		err = fmt.Errorf("%s", out.Error)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "supervisor env:", err)
		return
	}
	if len(out.Missing) > 0 {
		fmt.Fprintln(os.Stderr, "supervisor env: not available:", strings.Join(out.Missing, ", "))
	}
	keys := make([]string, 0, len(out.Env))
	for k := range out.Env {
		if envKey.MatchString(k) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("export %s=%s\n", k, shellQuote(out.Env[k]))
	}
}

// ambientGitToken: the ambient token for a GitHub owner, if there is one.
func ambientGitToken(owner string) (string, bool) {
	if owner == "" {
		return "", false
	}
	var out struct{ Token, Error string }
	status, err := getJSON("/ambient/git?owner="+url.QueryEscape(strings.ToLower(owner)), &out)
	switch {
	case err != nil:
		fmt.Fprintln(os.Stderr, "supervisor: ambient GitHub token:", err)
	case status == 200 && out.Token != "":
		return out.Token, true
	case status != 404:
		fmt.Fprintf(os.Stderr, "supervisor: ambient GitHub token for %s: %s\n", owner, out.Error)
	}
	return "", false
}

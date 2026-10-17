package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// GitHub access in Eldar's VM, as Eldar's GitHub App (branches and PRs on
// the staging repo; see server.GitHubApp). The supervisor holds the App's key;
// these ask it for a short-lived token.
//
//	supervisor git-credential get   git's credential helper for github.com
//	supervisor github-token         print a token, e.g. GH_TOKEN=$(supervisor github-token) gh pr create

func githubToken() (string, error) {
	url := os.Getenv("SUPERVISOR_URL")
	if url == "" {
		url = "http://10.42.0.1:9100"
	}
	c := &http.Client{Timeout: 30 * time.Second}
	res, err := c.Get(url + "/github/token")
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var out struct{ Token, Error string }
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("supervisor answered %s", res.Status)
	}
	if out.Token == "" {
		return "", fmt.Errorf("%s", out.Error)
	}
	return out.Token, nil
}

func runGitHubToken() {
	tok, err := githubToken()
	if err != nil {
		fmt.Fprintln(os.Stderr, "supervisor github-token:", err)
		os.Exit(1)
	}
	fmt.Println(tok)
}

// runGitCredential implements git's credential helper protocol: answer "get"
// for https://github.com, ignore "store" and "erase" (tokens expire anyway).
func runGitCredential(args []string) {
	if len(args) != 1 || args[0] != "get" {
		return
	}
	attrs := map[string]string{}
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), "="); ok {
			attrs[k] = v
		}
	}
	if attrs["protocol"] != "https" || attrs["host"] != "github.com" {
		return
	}
	tok, err := githubToken()
	if err != nil {
		fmt.Fprintln(os.Stderr, "supervisor git-credential:", err)
		return // git falls through to its other helpers, or fails
	}
	fmt.Printf("username=x-access-token\npassword=%s\n", tok)
}

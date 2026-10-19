package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// GitHub access in Eldar's VM. Two kinds of token, tried in this order after
// one a command was given explicitly ($GITHUB_TOKEN, injected by an approved
// supervisor exec):
//
//   - an ambient token for the repo's owner, if ambient.yaml lists one
//     (ambient.go): the org's fine-grained PAT, used without asking;
//   - Eldar's GitHub App (server.GitHubApp), which can only propose changes to
//     the staging repo; the supervisor holds its key and mints short-lived
//     tokens.
//
//	supervisor git-credential get   git's credential helper for github.com
//	                                (needs credential.useHttpPath for the owner)
//	supervisor github-token [OWNER] print a token, e.g. GH_TOKEN=$(supervisor github-token acme) gh ...

func githubToken() (string, error) {
	var out struct{ Token, Error string }
	if _, err := getJSON("/github/token", &out); err != nil {
		return "", err
	}
	if out.Token == "" {
		return "", fmt.Errorf("%s", out.Error)
	}
	return out.Token, nil
}

func runGitHubToken(args []string) {
	if len(args) > 0 {
		if tok, ok := ambientGitToken(args[0]); ok {
			fmt.Println(tok)
			return
		}
	}
	tok, err := githubToken()
	if err != nil {
		fmt.Fprintln(os.Stderr, "supervisor github-token:", err)
		os.Exit(1)
	}
	fmt.Println(tok)
}

// runGitCredential implements git's credential helper protocol: answer "get"
// for https://github.com, ignore "store" and "erase" (the supervisor is the
// source of truth, and App tokens expire anyway).
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
	answer := func(tok string) { fmt.Printf("username=x-access-token\npassword=%s\n", tok) }
	// A token this command was given (an approved exec) is a deliberate choice.
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		answer(tok)
		return
	}
	// path is "owner/repo.git" when credential.useHttpPath is set.
	owner, _, _ := strings.Cut(attrs["path"], "/")
	if tok, ok := ambientGitToken(owner); ok {
		answer(tok)
		return
	}
	tok, err := githubToken()
	if err != nil {
		fmt.Fprintln(os.Stderr, "supervisor git-credential:", err)
		return // git falls through to its other helpers, or fails
	}
	answer(tok)
}

package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/benjaminwood/eldar-os/supervisor/api"
)

// runAwsLogin is `supervisor aws-login [session]`: sign the supervisor in to
// AWS SSO. Prints a link and code to approve in a browser (any device), then
// waits. One sign-in covers every profile of that SSO session.
func runAwsLogin(args []string) {
	session := ""
	if len(args) > 0 {
		session = args[0]
	}
	path, err := api.FindSocket()
	if err != nil {
		fmt.Fprintln(os.Stderr, "supervisor aws-login:", err)
		os.Exit(1)
	}
	c := api.New(path)
	ctx := context.Background()
	l, err := c.AwsLogin(ctx, session)
	if err != nil {
		fmt.Fprintln(os.Stderr, "supervisor aws-login:", err)
		os.Exit(1)
	}
	fmt.Printf("Sign the supervisor in to AWS (%s):\n\n  %s\n\n  code: %s\n\nWaiting for approval (until %s)…\n",
		l.Session, l.VerificationURL, l.UserCode, l.ExpiresAt.Local().Format("15:04"))
	for {
		time.Sleep(3 * time.Second)
		st, err := c.AwsStatus(ctx)
		if err != nil {
			continue
		}
		for _, s := range st {
			if s.Session != l.Session || s.Login == nil || s.Login.Status == "pending" {
				continue
			}
			if s.Login.Status == "done" {
				fmt.Println("Signed in:", s.Login.Detail)
				return
			}
			fmt.Fprintln(os.Stderr, "Sign-in failed:", s.Login.Detail)
			os.Exit(1)
		}
	}
}

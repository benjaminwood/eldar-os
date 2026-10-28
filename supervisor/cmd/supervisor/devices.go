package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/benjaminwood/eldar-os/supervisor/api"
)

// runDevices lists paired devices (a Stream Deck, ...) or unpairs one.
// Devices pair themselves; Ben approves each pairing like any other request.
//
//	supervisor devices
//	supervisor devices unpair ID
func runDevices(args []string) {
	sock, err := api.FindSocket()
	if err != nil {
		fmt.Fprintln(os.Stderr, "supervisor devices:", err)
		os.Exit(1)
	}
	c := api.New(sock)
	c.Name = "supervisor devices"
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	switch {
	case len(args) == 0:
		st, err := c.State(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, "supervisor devices:", err)
			os.Exit(1)
		}
		if len(st.Devices) == 0 {
			fmt.Println("No devices paired.")
		}
		for _, d := range st.Devices {
			seen := "never"
			if d.LastSeen > 0 {
				seen = time.UnixMilli(d.LastSeen).Format("2006-01-02 15:04")
			}
			host := d.Host
			if host == "" {
				host = "-"
			}
			fmt.Printf("#%d  %-20s node %-20s host %-8s paired %s  last seen %s\n", d.ID, d.Name, d.Node, host,
				time.UnixMilli(d.CreatedAt).Format("2006-01-02"), seen)
		}
	case len(args) == 2 && args[0] == "unpair":
		id, err := strconv.Atoi(args[1])
		if err == nil {
			err = c.RemoveDevice(ctx, id)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "supervisor devices:", err)
			os.Exit(1)
		}
		fmt.Printf("Device #%d unpaired.\n", id)
	default:
		fmt.Fprintln(os.Stderr, "usage: supervisor devices [unpair ID]")
		os.Exit(2)
	}
}

package main

import (
	"encoding/json"
	"net/http"
)

// controlHandler plays the other side of the supervisor for a scripted demo
// (scripts/promo): agents filing requests, Eldar replying, sessions changing
// state. Loopback TCP only, and only with -control.
//
//	POST /demo/request {session, command, credentials, …}  → {id, ran}
//	POST /demo/eldar   {text}                               → the message
//	POST /demo/status  {session, status}
func (d *demo) controlHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /demo/request", func(w http.ResponseWriter, r *http.Request) {
		var b request
		json.NewDecoder(r.Body).Decode(&b)
		id, ran := d.file(b)
		send(w, map[string]any{"id": id, "ran": ran})
	})
	mux.HandleFunc("POST /demo/eldar", func(w http.ResponseWriter, r *http.Request) {
		var b struct{ Text string }
		json.NewDecoder(r.Body).Decode(&b)
		send(w, d.addChat("eldar", b.Text))
	})
	mux.HandleFunc("POST /demo/status", func(w http.ResponseWriter, r *http.Request) {
		var b struct{ Session, Status string }
		json.NewDecoder(r.Body).Decode(&b)
		d.setStatus(b.Session, b.Status)
		send(w, map[string]any{"ok": true})
	})
	return mux
}

package web

import (
	"context"
	"log"
	"net/http"
	"strconv"

	"github.com/coder/websocket"
)

// terminal relays a browser WebSocket to the admin API's live terminal.
// requireUnlock has checked this device's Face ID unlock (passkey.go).
// websocket.Accept enforces a same-origin Origin header, so a page on another
// site can't open one in Ben's browser.
func (s *server) terminal(w http.ResponseWriter, r *http.Request) {
	device := deviceOf(r)
	pane := r.URL.Query().Get("pane")
	cols, _ := strconv.Atoi(r.URL.Query().Get("cols"))
	rows, _ := strconv.Atoi(r.URL.Query().Get("rows"))

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	upstream, err := s.cfg.Client.DialTerminal(ctx, pane, cols, rows, "web · "+device)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer upstream.CloseNow()

	browser, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionContextTakeover})
	if err != nil {
		return
	}
	defer browser.CloseNow()
	browser.SetReadLimit(1 << 20)
	log.Printf("terminal open: %s → %s", device, pane)

	pipe := func(dst, src *websocket.Conn) {
		defer cancel()
		for {
			typ, data, err := src.Read(ctx)
			if err != nil {
				return
			}
			if err := dst.Write(ctx, typ, data); err != nil {
				return
			}
		}
	}
	go pipe(upstream, browser)
	pipe(browser, upstream)
	_ = upstream.Close(websocket.StatusNormalClosure, "")
	_ = browser.Close(websocket.StatusNormalClosure, "")
	log.Printf("terminal closed: %s → %s", device, pane)
}

package api

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/coder/websocket"
)

// DialTerminal opens the admin API's live terminal on a Claude session's pane.
// Binary messages are terminal bytes; text messages are JSON control frames
// ({"type":"resize","cols":N,"rows":N}).
func (c *Client) DialTerminal(ctx context.Context, paneID string, cols, rows int, clientName string) (*websocket.Conn, error) {
	q := url.Values{}
	q.Set("pane", paneID)
	q.Set("cols", strconv.Itoa(cols))
	q.Set("rows", strconv.Itoa(rows))
	h := http.Header{}
	if clientName == "" {
		clientName = c.Name
	}
	if clientName != "" {
		h.Set("X-Admin-Client", clientName)
	}
	conn, _, err := websocket.Dial(ctx, "ws://admin/admin/terminal?"+q.Encode(), &websocket.DialOptions{
		HTTPClient: c.stream,
		HTTPHeader: h,
	})
	if err != nil {
		return nil, friendly(err)
	}
	conn.SetReadLimit(1 << 20)
	return conn, nil
}

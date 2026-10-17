package mcp

import (
	"context"
	"strings"
	"testing"
)

func TestReadEvents(t *testing.T) {
	stream := ": ping\n\nevent: relay\ndata: {\"id\":1,\"text\":\"hi\"}\n\nevent: other\ndata: {\"id\":2}\n\nevent: relay\ndata: {\"id\":3,\n" +
		"data: \"text\":\"two lines\"}\n\n"
	var got []map[string]any
	readEvents(strings.NewReader(stream), "relay", func(m map[string]any) { got = append(got, m) })
	if len(got) != 2 || got[0]["text"] != "hi" || got[1]["text"] != "two lines" {
		t.Fatalf("got %v", got)
	}
}

func TestChannelsDeclareClaudeChannel(t *testing.T) {
	ch := &channel{c: &client{}, key: "k"}
	for _, s := range []*server{ch.chatServer("t"), ch.relayServer("t")} {
		r, err := s.handle(context.Background(), rpcRequest{Method: "initialize", Params: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		caps := r.(map[string]any)["capabilities"].(map[string]any)
		if _, ok := caps["experimental"].(map[string]any)["claude/channel"]; !ok || r.(map[string]any)["instructions"] == "" {
			t.Fatalf("%s: %v", s.name, r)
		}
	}
	tools, _ := ch.chatServer("t").handle(context.Background(), rpcRequest{Method: "tools/list"})
	if n := len(tools.(map[string]any)["tools"].([]map[string]any)); n != 2 {
		t.Fatalf("chat tools: %d", n)
	}
}

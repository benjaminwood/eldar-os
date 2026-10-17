// Package mcp holds the MCP servers agents in Eldar's VM use to reach the
// supervisor, all stdio and all in the `supervisor` binary:
//
//	supervisor mcp            every session: supervisor_exec, grants, notify_ben, …
//	supervisor channel chat   Eldar's session only: Ben's chat (a Claude channel)
//	supervisor channel relay  Eldar's session only: other agents' notify_ben events
//
// They hold nothing themselves; every tool is a call to the supervisor's
// agent API. The protocol is small enough to speak directly: newline-
// delimited JSON-RPC 2.0 on stdin/stdout, with initialize, tools/list and
// tools/call, plus notifications/claude/channel for the channels.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
)

const protocolVersion = "2025-06-18"

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// Run serves the supervisor's tools on stdin/stdout until stdin closes.
func Run(version string) error {
	s := newServer(newClient(), version)
	openScratchpadToBen(s.c.sessionID)
	return s.serve(context.Background(), os.Stdin, os.Stdout)
}

type server struct {
	c            *client
	name         string
	version      string
	instructions string
	channel      bool // a Claude channel: declares claude/channel, may push events
	tools        map[string]tool
	hostsMu      sync.Mutex
	hosts        []hostInfo // remote hosts, as the supervisor last reported them
	describe     func(ctx context.Context) []map[string]any
	// start runs alongside the protocol loop (a channel's event stream);
	// notify sends a notification to Claude.
	start func(ctx context.Context, notify func(method string, params any))
}

func newServer(c *client, version string) *server {
	s := &server{c: c, name: "supervisor", version: version, tools: map[string]tool{}}
	for _, t := range s.toolList() {
		s.tools[t.Name] = t
	}
	s.describe = s.describeTools
	return s
}

func (s *server) serve(ctx context.Context, in io.Reader, out io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var mu sync.Mutex
	enc := json.NewEncoder(out)
	write := func(v any) {
		mu.Lock()
		defer mu.Unlock()
		enc.Encode(v)
	}
	send := func(r rpcResponse) { write(r) }
	notify := func(method string, params any) {
		write(rpcNotification{JSONRPC: "2.0", Method: method, Params: params})
	}
	if s.start != nil {
		go s.start(ctx, notify)
	}
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64*1024), 16<<20)
	var wg sync.WaitGroup
	for sc.Scan() {
		var req rpcRequest
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			send(rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32700, "parse error"}})
			continue
		}
		if len(req.ID) == 0 {
			continue // a notification: nothing to answer
		}
		// Calls can wait minutes for Ben; answer each on its own.
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := s.handle(ctx, req)
			r := rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result}
			if err != nil {
				r.Result, r.Error = nil, err
			}
			send(r)
		}()
	}
	wg.Wait()
	return sc.Err()
}

func (s *server) handle(ctx context.Context, req rpcRequest) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(req.Params, &p)
		v := p.ProtocolVersion
		if v == "" {
			v = protocolVersion
		}
		caps := map[string]any{"tools": map[string]any{}}
		if s.channel {
			caps["experimental"] = map[string]any{"claude/channel": map[string]any{}}
		}
		r := map[string]any{
			"protocolVersion": v,
			"capabilities":    caps,
			"serverInfo":      map[string]any{"name": s.name, "version": s.version},
		}
		if s.instructions != "" {
			r["instructions"] = s.instructions
		}
		return r, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		tools := []map[string]any{}
		if s.describe != nil {
			tools = s.describe(ctx)
		}
		return map[string]any{"tools": tools}, nil
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{-32602, "invalid params"}
		}
		t, ok := s.tools[p.Name]
		if !ok {
			return errorText(fmt.Sprintf("Unknown tool: %s", p.Name)), nil
		}
		if p.Arguments == nil {
			p.Arguments = map[string]any{}
		}
		return t.run(ctx, p.Arguments), nil
	}
	return nil, &rpcError{-32601, "method not found: " + req.Method}
}

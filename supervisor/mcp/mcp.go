// Package mcp is the supervisor's MCP server for Claude sessions in Eldar's
// VM (`supervisor mcp`, stdio): the agents' way to run commands with
// credentials, ask for grants, and check policy. It holds nothing itself;
// every tool is a call to the supervisor's agent API.
//
// The protocol is small enough to speak directly: newline-delimited
// JSON-RPC 2.0 on stdin/stdout, with initialize, tools/list and tools/call.
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

// Run serves MCP on stdin/stdout until stdin closes.
func Run(version string) error {
	s := newServer(newClient(), version)
	openScratchpadToBen(s.c.sessionID)
	return s.serve(context.Background(), os.Stdin, os.Stdout)
}

type server struct {
	c       *client
	version string
	tools   map[string]tool
}

func newServer(c *client, version string) *server {
	s := &server{c: c, version: version, tools: map[string]tool{}}
	for _, t := range s.toolList() {
		s.tools[t.Name] = t
	}
	return s
}

func (s *server) serve(ctx context.Context, in io.Reader, out io.Writer) error {
	var mu sync.Mutex
	enc := json.NewEncoder(out)
	send := func(r rpcResponse) {
		mu.Lock()
		defer mu.Unlock()
		enc.Encode(r)
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
		return map[string]any{
			"protocolVersion": v,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "supervisor", "version": s.version},
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": s.describeTools(ctx)}, nil
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

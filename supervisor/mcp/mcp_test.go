package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeSupervisor answers /status and records /exec bodies.
func fakeSupervisor(t *testing.T, exec func(body map[string]any) map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var out map[string]any
		switch r.URL.Path {
		case "/status":
			out = map[string]any{"op": map[string]any{"defaultProfile": "work",
				"profiles": []any{map[string]any{"name": "work"}, map[string]any{"name": "home"}}}}
		case "/exec":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			out = exec(body)
		default:
			w.WriteHeader(404)
			out = map[string]any{"error": "Not found"}
		}
		json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// call sends JSON-RPC requests through the server and returns the responses by id.
func call(t *testing.T, srv *httptest.Server, reqs ...string) map[float64]map[string]any {
	t.Helper()
	s := newServer(&client{url: srv.URL, sessionID: "sess-1", sessionKey: "key-1", http: srv.Client()}, "test")
	var out strings.Builder
	if err := s.serve(context.Background(), strings.NewReader(strings.Join(reqs, "\n")+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	got := map[float64]map[string]any{}
	sc := bufio.NewScanner(strings.NewReader(out.String()))
	for sc.Scan() {
		var r map[string]any
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("bad response %q: %v", sc.Text(), err)
		}
		got[r["id"].(float64)] = r
	}
	return got
}

func resultText(t *testing.T, r map[string]any) (string, bool) {
	t.Helper()
	res := r["result"].(map[string]any)
	isErr, _ := res["isError"].(bool)
	return res["content"].([]any)[0].(map[string]any)["text"].(string), isErr
}

func TestToolsListNamesProfilesFromSupervisor(t *testing.T) {
	srv := fakeSupervisor(t, nil)
	got := call(t, srv,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if v := got[1]["result"].(map[string]any)["protocolVersion"]; v != "2025-06-18" {
		t.Fatalf("protocolVersion = %v", v)
	}
	tools := got[2]["result"].(map[string]any)["tools"].([]any)
	var exec map[string]any
	for _, x := range tools {
		if m := x.(map[string]any); m["name"] == "supervisor_exec" {
			exec = m
		}
		if name := x.(map[string]any)["name"].(string); !strings.HasPrefix(name, "supervisor_") {
			t.Errorf("tool %q isn't named supervisor_*", name)
		}
	}
	profile := exec["inputSchema"].(map[string]any)["properties"].(map[string]any)["profile"].(map[string]any)
	if profile["default"] != "work" || len(profile["enum"].([]any)) != 2 {
		t.Fatalf("profile schema = %v", profile)
	}
}

func TestExecSendsScriptVerbatimWithSession(t *testing.T) {
	var sent map[string]any
	srv := fakeSupervisor(t, func(b map[string]any) map[string]any {
		sent = b
		return map[string]any{"exitCode": 3, "stdout": "hi\n", "stderr": "oops"}
	})
	cmd := `echo "$TOKEN" | jq '.a' <<'EOF'` + "\n{}\nEOF"
	args, _ := json.Marshal(map[string]any{"command": cmd, "reason": "test", "credentials": []string{}})
	got := call(t, srv, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"supervisor_exec","arguments":`+string(args)+`}}`)
	if sent["script"] != cmd || sent["profile"] != "work" || sent["sessionId"] != "sess-1" || sent["sessionKey"] != "key-1" {
		t.Fatalf("sent %v", sent)
	}
	if c, ok := sent["credentials"].([]any); !ok || len(c) != 0 {
		t.Fatalf("credentials [] must be sent as [], got %v", sent["credentials"])
	}
	text, isErr := resultText(t, got[7])
	if !isErr || !strings.Contains(text, "hi\n") || !strings.Contains(text, "--- stderr ---\noops") || !strings.Contains(text, "[exit code: 3]") {
		t.Fatalf("result %q isErr=%v", text, isErr)
	}
}

func TestExecFailureCarriesOutcomeGuidance(t *testing.T) {
	srv := fakeSupervisor(t, func(map[string]any) map[string]any {
		return map[string]any{"error": "Ben said no", "outcome": "denied"}
	})
	got := call(t, srv, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"supervisor_exec","arguments":{"command":"x","reason":"y"}}}`)
	text, isErr := resultText(t, got[1])
	if !isErr || !strings.HasPrefix(text, "SUPERVISOR REQUEST DENIED") || !strings.Contains(text, guidance["denied"]) {
		t.Fatalf("result %q", text)
	}
}

func TestUnreachableSupervisor(t *testing.T) {
	srv := fakeSupervisor(t, nil)
	srv.Close()
	got := call(t, srv, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"supervisor_status","arguments":{}}}`)
	if text, _ := resultText(t, got[1]); !strings.HasPrefix(text, "SUPERVISOR UNREACHABLE") {
		t.Fatalf("result %q", text)
	}
}


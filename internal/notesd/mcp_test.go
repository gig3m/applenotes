package notesd

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Written before the MCP endpoint. Hermes already speaks this shape to
// applereminders on the same Mac, so the contract is: JSON-RPC 2.0 over POST at
// /mcp, behind the same bearer token as everything else.

func rpc(t *testing.T, srv *testServer, method, params string) map[string]any {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"` + method + `"`
	if params != "" {
		body += `,"params":` + params
	}
	body += `}`
	rec := srv.do(t, "POST", "/mcp", body, "Bearer "+testToken)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s: not JSON: %q", method, rec.Body)
	}
	return out
}

// Property: /mcp is behind the same token as the REST routes. An MCP endpoint
// that forgot auth would be the whole library, unauthenticated.
func TestMCPRequiresAuth(t *testing.T) {
	srv := newTestServer(t)
	for _, auth := range []string{"", "Bearer wrong"} {
		rec := srv.do(t, "POST", "/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, auth)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("auth %q: got %d, want 401", auth, rec.Code)
		}
	}
}

// Property: initialize answers with a protocol version and server info.
func TestMCPInitialize(t *testing.T) {
	got := rpc(t, newTestServer(t), "initialize", `{"protocolVersion":"2024-11-05"}`)
	res, ok := got["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %v", got)
	}
	if res["protocolVersion"] == "" || res["serverInfo"] == nil {
		t.Errorf("incomplete initialize result: %v", res)
	}
}

// Property: every advertised tool has a name, a description and a schema. A
// tool with no schema is unusable by a model.
func TestMCPToolsAreFullyDescribed(t *testing.T) {
	got := rpc(t, newTestServer(t), "tools/list", "")
	res := got["result"].(map[string]any)
	tools, _ := res["tools"].([]any)
	if len(tools) == 0 {
		t.Fatal("no tools advertised")
	}
	for _, tool := range tools {
		m := tool.(map[string]any)
		for _, field := range []string{"name", "description", "inputSchema"} {
			if m[field] == nil || m[field] == "" {
				t.Errorf("tool %v is missing %s", m["name"], field)
			}
		}
	}
}

// Property: a read tool returns the library.
func TestMCPListNotes(t *testing.T) {
	got := rpc(t, newTestServer(t), "tools/call", `{"name":"list_notes","arguments":{}}`)
	res, ok := got["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %v", got)
	}
	body, _ := json.Marshal(res)
	if !strings.Contains(string(body), "UUID-PLAIN") {
		t.Errorf("the note list is missing: %s", body)
	}
}

// Property: a tool error is reported as a tool error, not a transport failure.
// A model needs to see "that note does not exist", not a broken connection.
func TestMCPToolErrorsAreInBand(t *testing.T) {
	got := rpc(t, newTestServer(t), "tools/call", `{"name":"get_note","arguments":{"uuid":"UUID-NOSUCH"}}`)
	if got["error"] != nil {
		t.Fatalf("a missing note became a protocol error: %v", got["error"])
	}
	res := got["result"].(map[string]any)
	if res["isError"] != true {
		t.Errorf("the failure was not flagged: %v", res)
	}
}

// Property: an unknown method is a JSON-RPC error with the right code.
func TestMCPUnknownMethod(t *testing.T) {
	got := rpc(t, newTestServer(t), "nonesuch", "")
	e, ok := got["error"].(map[string]any)
	if !ok {
		t.Fatalf("no error for an unknown method: %v", got)
	}
	if e["code"].(float64) != -32601 {
		t.Errorf("code %v, want -32601 (method not found)", e["code"])
	}
}

// Property: a write tool refusing to destroy content says so in band, with what
// would be lost -- the same guarantee the REST side gives.
func TestMCPWriteRefusalIsInBand(t *testing.T) {
	got := rpc(t, newTestServer(t), "tools/call",
		`{"name":"replace_note","arguments":{"uuid":"UUID-ATTACH","markdown":"x"}}`)
	res, ok := got["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %v", got)
	}
	if res["isError"] != true {
		t.Errorf("a refused rewrite was not flagged: %v", res)
	}
	body, _ := json.Marshal(res)
	if !strings.Contains(string(body), "attachments") {
		t.Errorf("the refusal does not say what would be lost: %s", body)
	}
}

// Property: malformed JSON-RPC is a parse error, never a panic or a 5xx.
func TestMCPMalformedInput(t *testing.T) {
	srv := newTestServer(t)
	for _, body := range []string{"", "{", "null", `{"jsonrpc":"1.0"}`, `{"method":123}`} {
		rec := srv.do(t, "POST", "/mcp", body, "Bearer "+testToken)
		if rec.Code >= 500 {
			t.Errorf("%q: got %d", body, rec.Code)
		}
		if !json.Valid(rec.Body.Bytes()) {
			t.Errorf("%q: response is not JSON: %q", body, rec.Body)
		}
	}
}

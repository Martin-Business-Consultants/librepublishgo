package cms

import (
	"encoding/json"
	"strings"
	"testing"
)

// rpc is one JSON-RPC call to /mcp as token: its result (or error) body.
func (s *site) rpc(t *testing.T, token, method string, params any) map[string]any {
	t.Helper()
	r := s.must(200, s.do(token, "POST", "/mcp", map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params}))
	return r.Body
}

// toolText is a tools/call result's text, decoded.
func toolText(t *testing.T, res map[string]any) (map[string]any, bool) {
	t.Helper()
	result, _ := res["result"].(map[string]any)
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("no content: %v", res)
	}
	text := str(content[0].(map[string]any)["text"])
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		out = map[string]any{"text": text} // a refusal before the API: words, not JSON
	}
	isErr, _ := result["isError"].(bool)
	return out, isErr
}

// An AI app's session over MCP: it learns the tools, then works on the
// site through them, as the token's role.
func TestMCP(t *testing.T) {
	t.Parallel()
	s := newSite(t)

	// No token, no MCP.
	if r := s.do("", "POST", "/mcp", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize"}); r.Status != 401 {
		t.Fatalf("anonymous: %d", r.Status)
	}
	init := s.rpc(t, s.admin, "initialize", map[string]any{"protocolVersion": mcpVersion, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test"}})
	if dig(init, "result", "serverInfo", "name") != "librepublish" || !strings.Contains(str(dig(init, "result", "instructions")), "get_manifest") {
		t.Fatalf("initialize: %v", init)
	}
	// A notification is accepted, with nothing to say.
	if r := s.do(s.admin, "POST", "/mcp", map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}); r.Status != 202 {
		t.Fatalf("notification: %d", r.Status)
	}

	list := s.rpc(t, s.admin, "tools/list", map[string]any{})
	tools, _ := dig(list, "result", "tools").([]any)
	names := map[string]map[string]any{}
	for _, it := range tools {
		m := it.(map[string]any)
		names[str(m["name"])] = m
	}
	for _, want := range []string{"get_manifest", "list_pages", "get_page", "create_page", "update_page", "publish_page", "list_entries", "upload_asset", "update_collection_schema", "api_request"} {
		if names[want] == nil {
			t.Fatalf("tools/list lacks %s (has %d)", want, len(names))
		}
	}
	if req, _ := dig(names["get_page"], "inputSchema", "required").([]any); len(req) != 1 || req[0] != "path" {
		t.Fatalf("get_page's schema: %v", names["get_page"]["inputSchema"])
	}

	call := func(name string, args map[string]any) (map[string]any, bool) {
		return toolText(t, s.rpc(t, s.admin, "tools/call", map[string]any{"name": name, "arguments": args}))
	}
	man, isErr := call("get_manifest", nil)
	if isErr || man["status"] != "ok" || man["summary"] == "" {
		t.Fatalf("get_manifest: %v", man)
	}
	made, isErr := call("create_page", map[string]any{"page": map[string]any{"title": "Our story", "slug": "story"}})
	if isErr || dig(made, "data", "page", "path") != "story" {
		t.Fatalf("create_page: %v", made)
	}
	if crumbs, _ := made["breadcrumbs"].([]any); len(crumbs) == 0 {
		t.Fatalf("no next steps: %v", made)
	}
	pub, isErr := call("publish_page", map[string]any{"path": "story"})
	if isErr || dig(pub, "data", "page", "status") != "published" {
		t.Fatalf("publish_page: %v", pub)
	}
	got, isErr := call("get_page", map[string]any{"path": "story"})
	if isErr || dig(got, "data", "page", "title") != "Our story" {
		t.Fatalf("get_page: %v", got)
	}
	// What the role can't do is the API's 403, as an error result.
	if _, isErr := toolText(t, s.rpc(t, s.agent, "tools/call", map[string]any{"name": "list_users", "arguments": map[string]any{}})); !isErr {
		t.Fatal("an agent token listed users")
	}
	// A missing path parameter, and a tool that isn't, say so.
	if _, isErr := call("get_page", map[string]any{}); !isErr {
		t.Fatal("get_page without a path")
	}
	if r := s.rpc(t, s.admin, "tools/call", map[string]any{"name": "nope", "arguments": map[string]any{}}); r["error"] == nil {
		t.Fatalf("unknown tool: %v", r)
	}
	// api_request reaches the rest, and only /api.
	if out, isErr := call("api_request", map[string]any{"method": "GET", "path": "/api/redirects/all"}); isErr || out["status"] != "ok" {
		t.Fatalf("api_request: %v", out)
	}
	if _, isErr := call("api_request", map[string]any{"method": "GET", "path": "/files/x"}); !isErr {
		t.Fatal("api_request left /api")
	}
	// The catalogue the command line reads.
	cat := s.must(200, s.do(s.admin, "GET", "/api/agent/tools", nil))
	if ts, _ := cat.Body["tools"].([]any); len(ts) != len(tools) {
		t.Fatalf("catalogue %d tools, tools/list %d", len(ts), len(tools))
	}
}

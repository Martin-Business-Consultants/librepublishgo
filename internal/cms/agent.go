package cms

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/apierr"
)

// The agent surface: the tools an AI agent (or a person at a terminal) uses
// to work on the site. A tool is declared on its route (h(…, tool(…))), so
// the catalogue is the API that runs, not a description of it; the
// catalogue (GET /api/agent/tools) feeds `librepublish <tool>` and the MCP
// server (POST /mcp), which runs a call through the same routes, as the
// caller, with the caller's role: a tool can never do more than the API
// lets that caller do.

// toolSpec is one tool: what it's called, what it does, the route it runs,
// and what it takes.
type toolSpec struct {
	Name    string      `json:"name"`
	Summary string      `json:"summary"`
	Notes   string      `json:"notes,omitempty"`
	Method  string      `json:"method"`
	Path    string      `json:"path"`
	Params  []toolParam `json:"params"`
	// The body's own key ({"page": {…}}), filled from the param of that name.
	Wrap string `json:"wrap,omitempty"`
	// Fixed into the body under Wrap: what publish_page adds ({"status": "published"}).
	Preset map[string]any `json:"preset,omitempty"`
	// The body is multipart (a file upload).
	Multipart bool `json:"multipart,omitempty"`
}

// toolParam is one argument: in the path, the query or the body.
type toolParam struct {
	Name     string `json:"name"`
	In       string `json:"in"`   // path, query, body, file
	Type     string `json:"type"` // string, integer, boolean, object, array, file
	Desc     string `json:"description"`
	Required bool   `json:"required,omitempty"`
}

type toolOpt func(*toolSpec)

// tool declares the route's tool.
func tool(name, summary string, opts ...toolOpt) func(*route) {
	return func(r *route) {
		t := &toolSpec{Name: name, Summary: summary}
		for _, o := range opts {
			o(t)
		}
		r.tool = t
	}
}

func query(name, typ, desc string) toolOpt {
	return func(t *toolSpec) {
		t.Params = append(t.Params, toolParam{Name: name, In: "query", Type: typ, Desc: desc})
	}
}

// body is the JSON object the route takes, sent as {wrap: value}.
func body(wrap, desc string, required bool) toolOpt {
	return func(t *toolSpec) {
		t.Wrap = wrap
		t.Params = append(t.Params, toolParam{Name: wrap, In: "body", Type: "object", Desc: desc, Required: required})
	}
}

// bodyList is a JSON list the route takes, sent as {wrap: [...]}.
func bodyList(wrap, desc string) toolOpt {
	return func(t *toolSpec) {
		t.Wrap = wrap
		t.Params = append(t.Params, toolParam{Name: wrap, In: "body", Type: "array", Desc: desc, Required: true})
	}
}

func note(s string) toolOpt { return func(t *toolSpec) { t.Notes = s } }

// file is a multipart file field: a path from the command line, or
// {"filename", "content_base64"} over MCP.
func file(name, desc string) toolOpt {
	return func(t *toolSpec) {
		t.Multipart = true
		t.Params = append(t.Params, toolParam{Name: name, In: "file", Type: "file", Desc: desc, Required: true})
	}
}

// formField is a multipart text field beside the file.
func formField(name, desc string) toolOpt {
	return func(t *toolSpec) {
		t.Params = append(t.Params, toolParam{Name: name, In: "body", Type: "string", Desc: desc})
	}
}

var pathParam = regexp.MustCompile(`\{([a-z_]+)(\.\.\.)?\}`)

// register records a route's tool, its path parameters read off the pattern.
func (a *App) register(pattern string, t *toolSpec) {
	method, path, _ := strings.Cut(pattern, " ")
	t.Method, t.Path = method, path
	var params []toolParam
	for _, m := range pathParam.FindAllStringSubmatch(path, -1) {
		desc := "The " + strings.ReplaceAll(m[1], "_", " ") + "."
		if m[1] == "path" {
			desc = "The page's path, without the leading slash: about, menu/drinks (the home page is home)."
		}
		params = append(params, toolParam{Name: m[1], In: "path", Type: "string", Desc: desc, Required: true})
	}
	t.Params = append(params, t.Params...)
	// A save that's checked against what it was based on takes lock_version.
	if strings.Contains(t.Notes, "lock_version") {
		t.Params = append(t.Params, toolParam{Name: "lock_version", In: "body", Type: "integer", Desc: "From the read this is based on."})
	}
	if !slices.ContainsFunc(a.tools, func(o *toolSpec) bool { return o.Name == t.Name }) {
		a.tools = append(a.tools, t)
	}
}

// deriveTools adds the tools that are another's route with fields fixed:
// publishing is updating with status published.
func (a *App) deriveTools() {
	from := func(name string) *toolSpec {
		i := slices.IndexFunc(a.tools, func(t *toolSpec) bool { return t.Name == name })
		if i < 0 {
			return nil
		}
		return a.tools[i]
	}
	for _, d := range []struct{ base, name, summary, status string }{
		{"update_page", "publish_page", "Publish a page: it goes live (and to the website at its next build).", "published"},
		{"update_page", "unpublish_page", "Take a page off the site: it's a draft again.", "draft"},
		{"update_entry", "publish_entry", "Publish an entry.", "published"},
		{"update_entry", "unpublish_entry", "Take an entry off the site: it's a draft again.", "draft"},
	} {
		base := from(d.base)
		if base == nil {
			continue
		}
		t := &toolSpec{Name: d.name, Summary: d.summary, Method: base.Method, Path: base.Path, Wrap: base.Wrap, Preset: map[string]any{"status": d.status}}
		for _, p := range base.Params {
			if p.In == "path" {
				t.Params = append(t.Params, p)
			}
		}
		t.Params = append(t.Params, toolParam{Name: "lock_version", In: "body", Type: "integer", Desc: "From the read this is based on (optional)."})
		a.tools = append(a.tools, t)
	}
}

// apiRequestTool reaches whatever the named tools don't: any /api route.
var apiRequestTool = &toolSpec{
	Name: "api_request", Summary: "Call any /api route the named tools don't cover, as you, with your role.",
	Notes:  "path starts with /api/. GET /api/manifest lists what this site has. Prefer a named tool when there is one: its answer comes with next steps.",
	Method: "ANY", Path: "/api/…",
	Params: []toolParam{
		{Name: "method", In: "body", Type: "string", Desc: "GET, POST, PATCH, PUT or DELETE.", Required: true},
		{Name: "path", In: "body", Type: "string", Desc: "The route, e.g. /api/redirects/export.", Required: true},
		{Name: "query", In: "body", Type: "object", Desc: "Query parameters, name → value."},
		{Name: "body", In: "body", Type: "object", Desc: "The JSON body."},
	},
}

// catalogue is every tool, in the order declared, api_request last.
func (a *App) catalogue() []*toolSpec { return append(slices.Clone(a.tools), apiRequestTool) }

func (a *App) listTools(c *Ctx) error {
	tools := a.catalogue()
	c.Summary = fmt.Sprintf("%d tools.", len(tools))
	c.Crumbs = []Crumb{crumb("Start with the site's shape", "librepublish get_manifest")}
	return c.JSON(200, O("tools", tools, "version", a.cfg.Version))
}

// inputSchema is a tool's arguments as JSON Schema, for MCP.
func (t *toolSpec) inputSchema() map[string]any {
	props := map[string]any{}
	required := []string{}
	for _, p := range t.Params {
		s := map[string]any{"description": p.Desc}
		switch p.Type {
		case "file":
			s["type"] = "object"
			s["properties"] = map[string]any{"filename": map[string]any{"type": "string"}, "content_base64": map[string]any{"type": "string"}}
			s["required"] = []string{"filename", "content_base64"}
		case "":
			s["type"] = "string"
		default:
			s["type"] = p.Type
		}
		props[p.Name] = s
		if p.Required {
			required = append(required, p.Name)
		}
	}
	return map[string]any{"type": "object", "properties": props, "required": required}
}

// description is what MCP shows of a tool.
func (t *toolSpec) description() string {
	d := t.Summary
	if t.Notes != "" {
		d += "\n\n" + t.Notes
	}
	return d
}

// call runs t with args through the routes, authenticated as auth (the
// caller's own Authorization header): the answer's status and body, in
// the agent envelope.
func (a *App) call(ctx context.Context, t *toolSpec, args map[string]any, auth, remote string) (int, []byte, error) {
	method, path := t.Method, ""
	q := url.Values{}
	var payload io.Reader
	contentType := "application/json"
	if t == apiRequestTool {
		var err error
		if method, path, payload, err = rawRequest(args, q); err != nil {
			return 0, nil, err
		}
	} else {
		var err error
		if path, err = toolPath(t, args); err != nil {
			return 0, nil, err
		}
		for _, p := range t.Params {
			if v, ok := args[p.Name]; ok && v != nil && p.In == "query" {
				q.Set(p.Name, fmt.Sprint(v))
			}
		}
		if t.Multipart {
			payload, contentType, err = multipartBody(t, args)
		} else {
			payload, err = jsonBody(t, args)
		}
		if err != nil {
			return 0, nil, err
		}
	}
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	r, err := http.NewRequestWithContext(ctx, method, path, payload)
	if err != nil {
		return 0, nil, err
	}
	r.Header.Set("Authorization", auth)
	r.Header.Set("Accept", "application/json")
	r.Header.Set("X-Agent-Envelope", "1")
	if payload != nil {
		r.Header.Set("Content-Type", contentType)
	}
	r.RemoteAddr = remote
	w := &recorder{header: http.Header{}, status: 200}
	a.mux.ServeHTTP(w, r)
	return w.status, w.body.Bytes(), nil
}

// toolPath is t's route with its path parameters filled from args.
func toolPath(t *toolSpec, args map[string]any) (string, error) {
	var missing []string
	path := pathParam.ReplaceAllStringFunc(t.Path, func(m string) string {
		sub := pathParam.FindStringSubmatch(m)
		v := strings.Trim(fmt.Sprint(args[sub[1]]), "/")
		if args[sub[1]] == nil || v == "" {
			missing = append(missing, sub[1])
			return ""
		}
		if sub[2] == "" {
			return url.PathEscape(v)
		}
		parts := strings.Split(v, "/")
		for i := range parts {
			parts[i] = url.PathEscape(parts[i])
		}
		return strings.Join(parts, "/")
	})
	if len(missing) > 0 {
		return "", apierr.BadRequest("missing " + strings.Join(missing, ", "))
	}
	return path, nil
}

// rawRequest is api_request's call: its method, /api path, query (into q)
// and JSON body.
func rawRequest(args map[string]any, q url.Values) (string, string, io.Reader, error) {
	method, path := strings.ToUpper(str(args["method"])), str(args["path"])
	if !strings.HasPrefix(path, "/api/") {
		return "", "", nil, apierr.BadRequest("path must start with /api/")
	}
	if qs, ok := args["query"].(map[string]any); ok {
		for k, v := range qs {
			q.Set(k, fmt.Sprint(v))
		}
	}
	b, ok := args["body"]
	if !ok || b == nil {
		return method, path, nil, nil
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return "", "", nil, err
	}
	return method, path, bytes.NewReader(raw), nil
}

// jsonBody is the request body a tool sends: its body param under Wrap,
// with the preset fields.
func jsonBody(t *toolSpec, args map[string]any) (io.Reader, error) {
	if t.Wrap == "" && t.Preset == nil {
		return nil, nil
	}
	inner := map[string]any{}
	switch v := args[t.Wrap].(type) {
	case []any:
		raw, err := json.Marshal(map[string]any{t.Wrap: v})
		return bytes.NewReader(raw), err
	case map[string]any:
		maps.Copy(inner, v)
	case nil:
	default:
		return nil, apierr.BadRequest(t.Wrap + " must be a JSON object")
	}
	maps.Copy(inner, t.Preset)
	if lv, ok := args["lock_version"]; ok {
		inner["lock_version"] = lv
	}
	raw, err := json.Marshal(map[string]any{t.Wrap: inner})
	return bytes.NewReader(raw), err
}

// multipartBody is a file upload: the file param ({"filename",
// "content_base64"}) and the text fields.
func multipartBody(t *toolSpec, args map[string]any) (io.Reader, string, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, p := range t.Params {
		v, ok := args[p.Name]
		if !ok || v == nil {
			continue
		}
		switch p.In {
		case "file":
			f, _ := v.(map[string]any)
			name, data := str(f["filename"]), str(f["content_base64"])
			raw, err := base64.StdEncoding.DecodeString(data)
			if err != nil || name == "" {
				return nil, "", apierr.BadRequest(p.Name + ` must be {"filename": "…", "content_base64": "…"}`)
			}
			fw, err := mw.CreateFormFile(p.Name, name)
			if err != nil {
				return nil, "", err
			}
			if _, err := fw.Write(raw); err != nil {
				return nil, "", err
			}
		case "body":
			if err := mw.WriteField(p.Name, fmt.Sprint(v)); err != nil {
				return nil, "", err
			}
		}
	}
	if err := mw.Close(); err != nil {
		return nil, "", err
	}
	return &buf, mw.FormDataContentType(), nil
}

// recorder keeps a routed answer.
type recorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (r *recorder) Header() http.Header         { return r.header }
func (r *recorder) Write(b []byte) (int, error) { return r.body.Write(b) }
func (r *recorder) WriteHeader(s int)           { r.status = s }

// ---- MCP ------------------------------------------------------------------

// mcpVersion is the MCP revision this server speaks.
const mcpVersion = "2025-06-18"

const mcpInstructions = `This is a Libre Publish CMS. Start with get_manifest: the site, your role and what it can do, the collections and block types.
Answers come as {status, summary, data, breadcrumbs}: summary says what came back, breadcrumbs are the next calls worth making, filled in.
Rich text fields hold the editor's document (Tiptap JSON: {"type":"doc","content":[…]}); plain text fields refuse HTML.
Saves carry lock_version from the read they're based on; a 409 means someone else saved since: read again, then save.
A 403 means your role can't do that, not that the CMS can't.`

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// mcp is the MCP server, Streamable HTTP without streaming: each POST is
// one JSON-RPC message (or a batch), answered as JSON. A token is how it's
// reached: the Authorization header the AI app sends.
func (a *App) mcp(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "POST JSON-RPC here; this server doesn't stream.", http.StatusMethodNotAllowed)
		return
	}
	c := &Ctx{App: a, W: w, R: r}
	actor, err := a.authenticate(c)
	if err != nil || actor == nil || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		w.Header().Set("WWW-Authenticate", `Bearer realm="librepublish"`)
		writeJSON(w, 401, map[string]any{"error": "unauthorized", "message": "Send Authorization: Bearer <token> (Settings › Service tokens, or `librepublish login`)."})
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<20))
	if err != nil {
		writeJSON(w, 413, rpcError(nil, -32600, "the request is too large"))
		return
	}
	if trimmed := bytes.TrimSpace(raw); len(trimmed) > 0 && trimmed[0] == '[' {
		var batch []rpcRequest
		if err := json.Unmarshal(trimmed, &batch); err != nil {
			writeJSON(w, 400, rpcError(nil, -32700, "not JSON-RPC"))
			return
		}
		var out []any
		for _, req := range batch {
			if res := a.rpc(r, req); res != nil {
				out = append(out, res)
			}
		}
		if len(out) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		writeJSON(w, 200, out)
		return
	}
	var req rpcRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		writeJSON(w, 400, rpcError(nil, -32700, "not JSON-RPC"))
		return
	}
	res := a.rpc(r, req)
	if res == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeJSON(w, 200, res)
}

// rpc answers one message; nil for a notification.
func (a *App) rpc(r *http.Request, req rpcRequest) any {
	if len(req.ID) == 0 {
		return nil // a notification (notifications/initialized and kin)
	}
	ok := func(result any) any { return map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result} }
	switch req.Method {
	case "initialize":
		return ok(map[string]any{
			"protocolVersion": mcpVersion,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "librepublish", "title": "Libre Publish", "version": a.cfg.Version},
			"instructions":    mcpInstructions,
		})
	case "ping":
		return ok(map[string]any{})
	case "tools/list":
		var tools []any
		for _, t := range a.catalogue() {
			tools = append(tools, map[string]any{"name": t.Name, "description": t.description(), "inputSchema": t.inputSchema()})
		}
		return ok(map[string]any{"tools": tools})
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return rpcError(req.ID, -32602, "params must be {name, arguments}")
		}
		i := slices.IndexFunc(a.catalogue(), func(t *toolSpec) bool { return t.Name == p.Name })
		if i < 0 {
			return rpcError(req.ID, -32602, "no tool "+p.Name+": tools/list says which there are")
		}
		status, body, err := a.call(r.Context(), a.catalogue()[i], p.Arguments, r.Header.Get("Authorization"), r.RemoteAddr)
		if err != nil {
			text := err.Error()
			if e, isAPI := errors.AsType[*apierr.Error](err); isAPI {
				text = e.Message
			}
			return ok(map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "isError": true})
		}
		if len(body) == 0 {
			body = []byte(fmt.Sprintf(`{"status":"ok","summary":"Done (%d)."}`, status))
		}
		result := map[string]any{"content": []any{map[string]any{"type": "text", "text": string(body)}}, "isError": status >= 400}
		var structured map[string]any
		if json.Unmarshal(body, &structured) == nil {
			result["structuredContent"] = structured
		}
		return ok(result)
	}
	return rpcError(req.ID, -32601, "no method "+req.Method)
}

func rpcError(id json.RawMessage, code int, message string) any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

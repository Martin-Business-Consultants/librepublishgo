package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Env is where a command reads and writes, and this binary's version.
type Env struct {
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	Version        string
}

// ErrUnknown is a command that's neither librepublish's nor one of the
// site's tools.
var ErrUnknown = errors.New("unknown command")

// ErrFailed is a tool the site answered with an error (already printed).
var ErrFailed = errors.New("the tool failed")

// Commands are the client commands, for main's dispatch.
var Commands = []string{"login", "logout", "profiles", "whoami", "tools", "doctor", "mcp"}

// Run runs a client command, or a tool by name.
func Run(ctx context.Context, env Env, cmd string, args []string) error {
	switch cmd {
	case "login":
		return login(ctx, env, args)
	case "logout":
		return logout(env, args)
	case "profiles":
		return listProfiles(env)
	case "whoami":
		return runTool(ctx, env, "get_manifest", args)
	case "tools":
		return tools(ctx, env, args)
	case "doctor":
		return doctor(ctx, env, args)
	case "mcp":
		return mcp(ctx, env, args)
	}
	return runTool(ctx, env, cmd, args)
}

// flags pulls --profile and --json out of args.
func flags(args []string) (profile string, jsonOut bool, rest []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--json":
			jsonOut = true
		case a == "--profile" && i+1 < len(args):
			profile = args[i+1]
			i++
		case strings.HasPrefix(a, "--profile="):
			profile = strings.TrimPrefix(a, "--profile=")
		default:
			rest = append(rest, a)
		}
	}
	return profile, jsonOut, rest
}

// ---- login ------------------------------------------------------------------

func login(ctx context.Context, env Env, args []string) error {
	var site, name, token string
	noBrowser := false
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case (a == "--name" || a == "--profile") && i+1 < len(args):
			name, i = args[i+1], i+1
		case strings.HasPrefix(a, "--name="), strings.HasPrefix(a, "--profile="):
			name = a[strings.Index(a, "=")+1:]
		case a == "--token" && i+1 < len(args):
			token, i = args[i+1], i+1
		case a == "--no-browser":
			noBrowser = true
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("login: unknown flag %s", a)
		default:
			site = a
		}
	}
	if site == "" {
		return errors.New("usage: librepublish login https://cms.example.com [--profile NAME] [--token TOKEN] [--no-browser]")
	}
	if !strings.Contains(site, "://") {
		site = "https://" + site
	}
	u, err := url.Parse(site)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%q isn't a site's address", site)
	}
	site = strings.TrimRight(u.Scheme+"://"+u.Host+u.Path, "/")
	if name == "" {
		name = u.Hostname()
	}
	// Signing in again to the same site refreshes its token; a profile that
	// names another site is someone's credential, and is not overwritten.
	ps, err := loadProfiles()
	if err != nil {
		return err
	}
	if p, ok := ps.Profiles[name]; ok && p.URL != site {
		return fmt.Errorf("profile %q is already %s: sign in with another --profile, or remove it first (librepublish logout --profile %s)", name, p.URL, name)
	}
	c := newClient(Profile{URL: site})
	if token == "" {
		if token, err = deviceLogin(ctx, env, c, noBrowser); err != nil {
			return err
		}
	}
	c.Token = token
	if status, body, err := c.do(ctx, "GET", "/api/manifest", nil); err != nil {
		return err
	} else if status != 200 {
		return apiError(status, body)
	}
	ps.Profiles[name] = Profile{URL: site, Token: token}
	if ps.Default == "" || len(ps.Profiles) == 1 {
		ps.Default = name
	}
	if err := ps.save(); err != nil {
		return err
	}
	path, _ := configPath()
	fmt.Fprintf(env.Stdout, "Signed in to %s as profile %q (kept in %s).\n", site, name, path)
	if ps.Default != name {
		fmt.Fprintf(env.Stdout, "Use it with --profile %s, or make it the default: librepublish profiles --default %s\n", name, name)
	}
	fmt.Fprintln(env.Stdout, "Next: librepublish whoami, librepublish tools, librepublish mcp setup")
	return nil
}

// deviceLogin asks the site for a code, sends the person to approve it in
// the admin, and waits for the token.
func deviceLogin(ctx context.Context, env Env, c *client, noBrowser bool) (string, error) {
	host, _ := os.Hostname()
	status, body, err := c.do(ctx, "POST", "/api/device/code", map[string]any{"hostname": host, "label": "librepublish on " + host})
	if err != nil {
		return "", err
	}
	if status != 200 {
		return "", apiError(status, body)
	}
	var code struct {
		UserCode, DeviceCode, VerificationURL string
		Interval, ExpiresIn                   int
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return "", err
	}
	code.UserCode, code.DeviceCode, code.VerificationURL = fmt.Sprint(raw["user_code"]), fmt.Sprint(raw["device_code"]), fmt.Sprint(raw["verification_url"])
	code.Interval = intOf(raw["interval"], 5)
	code.ExpiresIn = intOf(raw["expires_in"], 900)
	fmt.Fprintf(env.Stdout, "Approve this sign-in in the admin:\n\n    %s\n\nand check it shows the code %s. Waiting…\n", code.VerificationURL, code.UserCode)
	if !noBrowser {
		openBrowser(ctx, code.VerificationURL)
	}
	deadline := time.Now().Add(time.Duration(code.ExpiresIn) * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Duration(code.Interval) * time.Second):
		}
		status, body, err := c.do(ctx, "POST", "/api/device/token", map[string]any{"device_code": code.DeviceCode})
		if err != nil {
			return "", err
		}
		var res map[string]any
		_ = json.Unmarshal(body, &res)
		switch {
		case status == 200 && res["status"] == "ok":
			return fmt.Sprint(res["token"]), nil
		case status == 200:
			continue // still pending
		case res["error"] == "access_denied":
			return "", errors.New("the sign-in was denied in the admin")
		case res["error"] == "expired_token":
			return "", errors.New("the code expired: run librepublish login again")
		default:
			return "", apiError(status, body)
		}
	}
	return "", errors.New("the code expired: run librepublish login again")
}

// openBrowser opens the site's sign-in page (an argument, never a shell
// line); where it can't, the address printed above does.
func openBrowser(ctx context.Context, u string) {
	name, args := "xdg-open", []string{u}
	switch runtime.GOOS {
	case "darwin":
		name = "open"
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler", u}
	}
	_ = exec.CommandContext(ctx, name, args...).Start() //nolint:gosec // a fixed program, the site's own address
}

func intOf(v any, def int) int {
	if f, ok := v.(float64); ok && f > 0 {
		return int(f)
	}
	return def
}

func logout(env Env, args []string) error {
	ps, err := loadProfiles()
	if err != nil {
		return err
	}
	name := ps.Default
	if len(args) > 0 {
		name = strings.TrimPrefix(args[len(args)-1], "--profile=")
	}
	if _, ok := ps.Profiles[name]; !ok {
		return fmt.Errorf("no profile %q", name)
	}
	delete(ps.Profiles, name)
	if ps.Default == name {
		ps.Default = ""
		if names := sortedNames(ps.Profiles); len(names) > 0 {
			ps.Default = names[0]
		}
	}
	if err := ps.save(); err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "Signed out of %q (the token still works until it's revoked in the admin).\n", name)
	return nil
}

func listProfiles(env Env) error {
	ps, err := loadProfiles()
	if err != nil {
		return err
	}
	if len(ps.Profiles) == 0 {
		return ErrNoProfile
	}
	for _, n := range sortedNames(ps.Profiles) {
		mark := " "
		if n == ps.Default {
			mark = "*"
		}
		fmt.Fprintf(env.Stdout, "%s %-24s %s\n", mark, n, ps.Profiles[n].URL)
	}
	return nil
}

// ---- tools ------------------------------------------------------------------

func tools(ctx context.Context, env Env, args []string) error {
	profile, jsonOut, rest := flags(args)
	p, _, err := resolve(profile)
	if err != nil {
		return err
	}
	list, version, err := newClient(p).catalogue(ctx)
	if err != nil {
		return err
	}
	if jsonOut {
		return json.NewEncoder(env.Stdout).Encode(map[string]any{"tools": list, "version": version})
	}
	if len(rest) > 0 {
		i := slices.IndexFunc(list, func(t Tool) bool { return t.Name == toolName(rest[0]) })
		if i < 0 {
			return fmt.Errorf("no tool %s on %s", rest[0], p.URL)
		}
		printTool(env.Stdout, list[i])
		return nil
	}
	fmt.Fprintf(env.Stdout, "%s (Libre Publish %s): %d tools. `librepublish tools NAME` for one in full; `--json` for all of them.\n\n", p.URL, version, len(list))
	for _, t := range list {
		fmt.Fprintf(env.Stdout, "  %-26s %s\n", t.Name, t.Summary)
	}
	return nil
}

func printTool(w io.Writer, t Tool) {
	fmt.Fprintf(w, "librepublish %s", t.Name)
	for _, p := range t.Params {
		flag := "--" + strings.ReplaceAll(p.Name, "_", "-") + " " + strings.ToUpper(p.Type)
		if !p.Required {
			flag = "[" + flag + "]"
		}
		fmt.Fprint(w, " "+flag)
	}
	fmt.Fprintf(w, "\n\n%s\n", t.Summary)
	if t.Notes != "" {
		fmt.Fprintf(w, "\n%s\n", t.Notes)
	}
	if len(t.Params) > 0 {
		fmt.Fprintln(w)
		for _, p := range t.Params {
			fmt.Fprintf(w, "  --%-18s %s\n", strings.ReplaceAll(p.Name, "_", "-"), p.Desc)
		}
	}
	if t.Method != "ANY" {
		fmt.Fprintf(w, "\n(%s %s)\n", t.Method, t.Path)
	}
	fmt.Fprintln(w, "\nObjects and lists are JSON: a value, @file.json, or - for stdin. --json prints the answer as the site sent it.")
}

func toolName(s string) string { return strings.ReplaceAll(s, "-", "_") }

// runTool parses a tool's arguments against its catalogue entry, runs it on
// the site, and prints the answer.
func runTool(ctx context.Context, env Env, name string, args []string) error {
	profile, jsonOut, rest := flags(args)
	p, _, err := resolve(profile)
	if errors.Is(err, ErrNoProfile) {
		return fmt.Errorf("%w %q: not librepublish's, and not signed in to a site to look for a tool of that name (librepublish login https://cms.example.com)", ErrUnknown, name)
	}
	if err != nil {
		return err
	}
	c := newClient(p)
	list, _, err := c.catalogue(ctx)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(list, func(t Tool) bool { return t.Name == toolName(name) })
	if i < 0 {
		return fmt.Errorf("%w %q: not librepublish's, and %s has no such tool (librepublish tools lists them)", ErrUnknown, name, p.URL)
	}
	t := list[i]
	toolArgs, err := parseArgs(env, t, rest)
	if err != nil {
		return fmt.Errorf("%s: %w (librepublish tools %s)", t.Name, err, t.Name)
	}
	out, isErr, err := c.call(ctx, t.Name, toolArgs)
	if err != nil {
		return err
	}
	if jsonOut {
		fmt.Fprintln(env.Stdout, string(out))
	} else {
		printAnswer(env, out, isErr)
	}
	if isErr {
		return ErrFailed
	}
	return nil
}

// parseArgs reads --name value pairs (and positional values for the
// required path parameters, in order) into a tool's arguments.
func parseArgs(env Env, t Tool, args []string) (map[string]any, error) {
	out := map[string]any{}
	byName := map[string]int{}
	var positional []int
	for i, p := range t.Params {
		byName[p.Name] = i
		if p.In == "path" {
			positional = append(positional, i)
		}
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			if len(positional) == 0 {
				return nil, fmt.Errorf("unexpected %q", a)
			}
			p := t.Params[positional[0]]
			positional = positional[1:]
			out[p.Name] = a
			continue
		}
		key, val, hasVal := strings.Cut(strings.TrimPrefix(a, "--"), "=")
		key = toolName(key)
		idx, ok := byName[key]
		if !ok {
			return nil, fmt.Errorf("no --%s", strings.ReplaceAll(key, "_", "-"))
		}
		p := t.Params[idx]
		if !hasVal {
			switch {
			case p.Type == "boolean" && (i+1 >= len(args) || strings.HasPrefix(args[i+1], "--")):
				val = "true"
			case i+1 < len(args):
				val, i = args[i+1], i+1
			default:
				return nil, fmt.Errorf("--%s needs a value", a[2:])
			}
		}
		v, err := value(env, p.Type, val)
		if err != nil {
			return nil, fmt.Errorf("--%s: %w", strings.ReplaceAll(key, "_", "-"), err)
		}
		out[p.Name] = v
		positional = slices.DeleteFunc(positional, func(j int) bool { return j == idx })
	}
	for _, p := range t.Params {
		if _, ok := out[p.Name]; p.Required && !ok {
			return nil, fmt.Errorf("--%s is required", strings.ReplaceAll(p.Name, "_", "-"))
		}
	}
	return out, nil
}

// value is a command-line value as its type: JSON for objects and lists
// (inline, @file or - for stdin), a file's contents for a file.
func value(env Env, typ, raw string) (any, error) {
	switch typ {
	case "integer":
		return strconv.Atoi(raw)
	case "boolean":
		return strconv.ParseBool(raw)
	case "object", "array":
		data := []byte(raw)
		switch {
		case raw == "-":
			b, err := io.ReadAll(env.Stdin)
			if err != nil {
				return nil, err
			}
			data = b
		case strings.HasPrefix(raw, "@"):
			b, err := os.ReadFile(raw[1:])
			if err != nil {
				return nil, err
			}
			data = b
		}
		var v any
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, fmt.Errorf("isn't JSON: %w", err)
		}
		return v, nil
	case "file":
		b, err := os.ReadFile(raw) //nolint:gosec // the file the person named
		if err != nil {
			return nil, err
		}
		return map[string]any{"filename": filepath.Base(raw), "content_base64": base64.StdEncoding.EncodeToString(b)}, nil
	}
	return raw, nil
}

// printAnswer is an envelope for a person: the summary, the data, and the
// next steps.
func printAnswer(env Env, raw []byte, isErr bool) {
	var e struct {
		Status      string          `json:"status"`
		Summary     string          `json:"summary"`
		Data        json.RawMessage `json:"data"`
		Message     string          `json:"message"`
		Breadcrumbs []struct {
			Description string `json:"description"`
			Command     string `json:"command"`
		} `json:"breadcrumbs"`
	}
	if json.Unmarshal(raw, &e) != nil || (e.Summary == "" && e.Data == nil && e.Message == "") {
		w := env.Stdout
		if isErr {
			w = env.Stderr
		}
		fmt.Fprintln(w, strings.TrimSpace(string(raw)))
		return
	}
	if isErr {
		msg := e.Message
		if msg == "" {
			msg = e.Summary
		}
		if msg == "" {
			msg = string(raw)
		}
		fmt.Fprintln(env.Stderr, msg)
		return
	}
	if e.Summary != "" {
		fmt.Fprintln(env.Stdout, e.Summary)
	}
	if len(e.Data) > 0 && string(e.Data) != "null" {
		var pretty bytes.Buffer
		if json.Indent(&pretty, e.Data, "", "  ") == nil {
			fmt.Fprintln(env.Stdout, pretty.String())
		}
	}
	if len(e.Breadcrumbs) > 0 {
		fmt.Fprintln(env.Stdout, "\nNext:")
		for _, b := range e.Breadcrumbs {
			fmt.Fprintf(env.Stdout, "  %s\n    %s\n", b.Description, b.Command)
		}
	}
}

// ---- doctor -----------------------------------------------------------------

func doctor(ctx context.Context, env Env, args []string) error {
	profile, _, _ := flags(args)
	ok := true
	check := func(pass bool, what, fix string) {
		mark := "ok  "
		if !pass {
			mark, ok = "FAIL", false
		}
		fmt.Fprintf(env.Stdout, "%s %s\n", mark, what)
		if !pass && fix != "" {
			fmt.Fprintf(env.Stdout, "     %s\n", fix)
		}
	}
	p, name, err := resolve(profile)
	check(err == nil, "signed in to a site ("+name+")", "librepublish login https://cms.example.com")
	if err != nil {
		return ErrFailed
	}
	c := newClient(p)
	status, _, err := c.do(ctx, "GET", "/up", nil)
	check(err == nil && status == 200, p.URL+" answers", "is the address right, and is it running?")
	status, body, err := c.do(ctx, "GET", "/api/manifest", nil)
	check(err == nil && status == 200, "the token works", "sign in again: librepublish login "+p.URL)
	if err == nil && status == 200 {
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		if role, _ := m["role"].(map[string]any); role != nil {
			fmt.Fprintf(env.Stdout, "     as %v, role %v\n", m["user"], role["name"])
		}
	}
	list, version, err := c.catalogue(ctx)
	check(err == nil && len(list) > 0, fmt.Sprintf("its tools (%d)", len(list)), "")
	if err == nil {
		same := strings.TrimPrefix(version, "v") == strings.TrimPrefix(env.Version, "v")
		check(same || env.Version == "dev" || version == "dev", fmt.Sprintf("this librepublish (%s) and the site (%s) match", env.Version, version),
			"tools come from the site, so they work anyway; to match, run librepublish update (or install the site's version)")
	}
	status, _, err = c.do(ctx, "POST", "/mcp", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	check(err == nil && status == 200, "its MCP server answers", "")
	if !ok {
		return ErrFailed
	}
	fmt.Fprintln(env.Stdout, "\nAll good. AI apps: librepublish mcp setup")
	return nil
}

// ---- MCP --------------------------------------------------------------------

// mcp is a local MCP server for AI apps that start one (stdio): each
// message goes to the site's own /mcp, signed in as the profile. `mcp
// setup` says how to add it to an app.
func mcp(ctx context.Context, env Env, args []string) error {
	profile, _, rest := flags(args)
	if len(rest) > 0 && rest[0] == "setup" {
		return mcpSetup(env, profile, rest[1:])
	}
	p, _, err := resolve(profile)
	if err != nil {
		return err
	}
	c := newClient(p)
	in := bufio.NewScanner(env.Stdin)
	in.Buffer(make([]byte, 1<<20), 64<<20)
	for in.Scan() {
		line := bytes.TrimSpace(in.Bytes())
		if len(line) == 0 {
			continue
		}
		var msg any
		if err := json.Unmarshal(line, &msg); err != nil {
			fmt.Fprintln(env.Stdout, `{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"not JSON"}}`)
			continue
		}
		status, body, err := c.do(ctx, "POST", "/mcp", msg)
		if err != nil {
			fmt.Fprintf(env.Stderr, "librepublish mcp: %v\n", err)
			if id := idOf(msg); id != nil {
				out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32603, "message": err.Error()}})
				fmt.Fprintln(env.Stdout, string(out))
			}
			continue
		}
		if status == 202 || len(bytes.TrimSpace(body)) == 0 {
			continue // a notification
		}
		if status != 200 {
			if id := idOf(msg); id != nil {
				out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32603, "message": apiError(status, body).Error()}})
				fmt.Fprintln(env.Stdout, string(out))
			}
			continue
		}
		fmt.Fprintln(env.Stdout, string(bytes.TrimSpace(body)))
	}
	return in.Err()
}

func idOf(msg any) any {
	if m, ok := msg.(map[string]any); ok {
		return m["id"]
	}
	return nil
}

func mcpSetup(env Env, profile string, args []string) error {
	p, name, err := resolve(profile)
	if err != nil {
		return err
	}
	exe, _ := os.Executable()
	if exe == "" {
		exe = "librepublish"
	}
	server := "librepublish"
	if name != "" && name != "environment" {
		server = "librepublish-" + strings.ReplaceAll(strings.SplitN(name, ".", 2)[0], " ", "-")
	}
	local := fmt.Sprintf("%s mcp --profile %s", exe, name)
	cfg, _ := json.MarshalIndent(map[string]any{"mcpServers": map[string]any{server: map[string]any{"command": exe, "args": []string{"mcp", "--profile", name}}}}, "", "  ")
	which := ""
	if len(args) > 0 {
		which = args[0]
	}
	all := which == ""
	w := env.Stdout
	fmt.Fprintf(w, "Libre Publish at %s, as profile %q.\n", p.URL, name)
	if all || which == "claude-code" {
		fmt.Fprintf(w, "\nClaude Code, one command:\n\n    claude mcp add --scope user %s -- %s\n", server, local)
		fmt.Fprintf(w, "\n  or straight to the site, with a token (Settings › Service tokens, or your API token on Account):\n\n    claude mcp add --transport http %s %s/mcp --header \"Authorization: Bearer <token>\"\n", server, p.URL)
	}
	if all || which == "codex" {
		fmt.Fprintf(w, "\nCodex:\n\n    codex mcp add %s -- %s\n", server, local)
	}
	if all || which == "claude-desktop" || which == "cursor" || which == "other" {
		fmt.Fprintf(w, "\nClaude Desktop (Settings › Developer › Edit Config), Cursor (~/.cursor/mcp.json) and other apps that start a local server:\n\n%s\n", indent(string(cfg)))
	}
	return nil
}

func indent(s string) string {
	return "    " + strings.ReplaceAll(s, "\n", "\n    ")
}

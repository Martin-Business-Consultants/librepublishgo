// Package cli is librepublish working on a site from a terminal or an AI
// app, over that site's API: `librepublish login`, then any of the site's
// tools by name (`librepublish list_pages --status draft`), `librepublish
// tools` for what there is, `librepublish doctor` for whether it works, and
// `librepublish mcp` for an AI app that wants a local MCP server.
//
// The tools are the site's own (GET /api/agent/tools), so this always
// matches the version of the site it talks to, and a tool runs on the site
// as an MCP call (POST /mcp): the command line and AI apps go through the
// same code, as the signed-in person or token, with that role.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Profile is one site and the credential used on it.
type Profile struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

type profiles struct {
	Default  string             `json:"default"`
	Profiles map[string]Profile `json:"profiles"`
}

// configPath is where profiles are kept: ~/.config/librepublish/profiles.json
// (or the platform's config directory), readable by its owner alone.
func configPath() (string, error) {
	if p := os.Getenv("LIBREPUBLISH_CONFIG"); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "librepublish", "profiles.json"), nil
}

func loadProfiles() (*profiles, error) {
	path, err := configPath()
	if err != nil {
		return nil, err
	}
	ps := &profiles{Profiles: map[string]Profile{}}
	raw, err := os.ReadFile(path) //nolint:gosec // the user's own config
	if errors.Is(err, os.ErrNotExist) {
		return ps, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, ps); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if ps.Profiles == nil {
		ps.Profiles = map[string]Profile{}
	}
	return ps, nil
}

func (ps *profiles) save() error {
	path, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(ps, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	// WriteFile's mode is only for a new file: one made looser before (or
	// by an older build) is tightened too.
	return os.Chmod(path, 0o600)
}

// ErrNoProfile is a command that needs a site, run before `librepublish login`.
var ErrNoProfile = errors.New("not signed in to a site: run `librepublish login https://cms.example.com` (or set LIBREPUBLISH_URL and LIBREPUBLISH_TOKEN)")

// resolve is the site a command works on: LIBREPUBLISH_URL and
// LIBREPUBLISH_TOKEN when set (CI, one-offs), else the named profile
// (--profile, LIBREPUBLISH_PROFILE), else the default one.
func resolve(name string) (Profile, string, error) {
	if u, t := os.Getenv("LIBREPUBLISH_URL"), os.Getenv("LIBREPUBLISH_TOKEN"); u != "" && t != "" && name == "" {
		return Profile{URL: strings.TrimRight(u, "/"), Token: t}, "environment", nil
	}
	ps, err := loadProfiles()
	if err != nil {
		return Profile{}, "", err
	}
	if name == "" {
		name = os.Getenv("LIBREPUBLISH_PROFILE")
	}
	if name == "" {
		name = ps.Default
	}
	p, ok := ps.Profiles[name]
	if !ok || name == "" {
		if name != "" && len(ps.Profiles) > 0 {
			return Profile{}, "", fmt.Errorf("no profile %q: `librepublish profiles` lists them", name)
		}
		return Profile{}, "", ErrNoProfile
	}
	return p, name, nil
}

// client calls one site.
type client struct {
	Profile
	http *http.Client
}

func newClient(p Profile) *client {
	return &client{Profile: p, http: &http.Client{Timeout: 5 * time.Minute}}
}

// do is one request: its status and body.
func (c *client) do(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var r io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		r = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.URL+path, r)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	req.Header.Set("User-Agent", "librepublish")
	res, err := c.http.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("can't reach %s: %w", c.URL, err)
	}
	defer res.Body.Close()
	out, err := io.ReadAll(io.LimitReader(res.Body, 256<<20))
	return res.StatusCode, out, err
}

// Tool is one of the site's tools, as its catalogue has it.
type Tool struct {
	Name    string `json:"name"`
	Summary string `json:"summary"`
	Notes   string `json:"notes"`
	Method  string `json:"method"`
	Path    string `json:"path"`
	Params  []struct {
		Name     string `json:"name"`
		In       string `json:"in"`
		Type     string `json:"type"`
		Desc     string `json:"description"`
		Required bool   `json:"required"`
	} `json:"params"`
}

func (c *client) catalogue(ctx context.Context) ([]Tool, string, error) {
	status, body, err := c.do(ctx, "GET", "/api/agent/tools", nil)
	if err != nil {
		return nil, "", err
	}
	if status != 200 {
		return nil, "", apiError(status, body)
	}
	var out struct {
		Tools   []Tool `json:"tools"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, "", fmt.Errorf("the site's tools: %w", err)
	}
	return out.Tools, out.Version, nil
}

// call runs a tool on the site (as an MCP tools/call): the answer, in the
// envelope, and whether the site called it an error.
func (c *client) call(ctx context.Context, tool string, args map[string]any) ([]byte, bool, error) {
	status, body, err := c.do(ctx, "POST", "/mcp", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args}})
	if err != nil {
		return nil, false, err
	}
	if status != 200 {
		return nil, false, apiError(status, body)
	}
	var res struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, false, err
	}
	if res.Error != nil {
		return nil, false, errors.New(res.Error.Message)
	}
	var text string
	if len(res.Result.Content) > 0 {
		text = res.Result.Content[0].Text
	}
	return []byte(text), res.Result.IsError, nil
}

// apiError is an error answer in words.
func apiError(status int, body []byte) error {
	var e struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &e) == nil && e.Message != "" {
		if status == 401 {
			return fmt.Errorf("%s (%d): sign in again with `librepublish login`", e.Message, status)
		}
		return fmt.Errorf("%s (%d)", e.Message, status)
	}
	return fmt.Errorf("the site answered %d: %s", status, strings.TrimSpace(string(body[:min(len(body), 300)])))
}

// sortedNames is a map's keys, sorted.
func sortedNames[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

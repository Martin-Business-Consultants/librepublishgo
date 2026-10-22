// Package launch is the site's launch checklist (docs/launch-checklist.md):
// one screen, Tools › Launch, that says whether the site is ready to go live
// and stays ready. Most items check themselves — the CMS's own (Settings,
// email, deploy, tokens, files, content, globals, backup) and the other
// plugins' (Forms: captcha keys, recipients, hooks) — and say how they know;
// the few no machine can see are ticked by a person.
//
// It only shows: nothing here changes the site or blocks publishing. A site
// that isn't built with @librepublish/astro can remove it and install a
// checklist of its own.
package launch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/plugin"
)

const version = "1.0.0"

// manual are the items a person ticks: what no check can see.
var manual = []plugin.Check{
	{Key: "manual.domain", Group: "manual", Title: "The site's domain points at the new deploy, and the old one is noted for rollback", Launch: true},
	{Key: "manual.test_submission", Group: "manual", Title: "A real submission on each form arrived end to end (stored, emailed, sent on)", Launch: true},
	{Key: "manual.tracking", Group: "manual", Title: "Analytics and tags fire on the live site"},
	{Key: "manual.search_console", Group: "manual", Title: "The sitemap is submitted to Search Console"},
	{Key: "manual.tokens_rotated", Group: "manual", Title: "Tokens shared during setup (in chats, tickets, email) are revoked"},
}

// groupOrder is the order groups are shown in; others follow, by name.
var groupOrder = []string{"general", "email", "deploy", "tokens", "build", "media", "content", "globals", "forms", "site", "backup", "manual"}

// Module is the plugin, for the CMS to register.
func Module() *plugin.Module {
	read, write := "launch:read", "launch:write"
	return &plugin.Module{
		Manifest: plugin.Manifest{
			Key:         "launch",
			Name:        "Launch checklist",
			Version:     version,
			Description: "Whether the site is ready to go live, and stays ready: what the CMS and its plugins check themselves, with the evidence, and what a person ticks.",
			Permissions: plugin.Grants{SiteRead: true, Storage: true},
			Capabilities: []plugin.Capability{
				{Key: read, Label: "See the launch checklist"},
				{Key: write, Label: "Tick the launch checklist's manual items"},
			},
			Routes: []plugin.Route{
				{Method: "GET", Path: "/checks", Export: "checks", Capability: read},
				{Method: "GET", Path: "/check", Export: "check", Capability: read},
				{Method: "GET", Path: "/badge", Export: "badge", Capability: read},
				{Method: "POST", Path: "/manual", Export: "tick", Capability: write},
			},
			Admin: mustJSON(map[string]any{
				"menu": map[string]any{"label": "Launch", "icon": "check-circle", "group": "Tools", "badge": "/badge"},
				"screens": []map[string]any{
					{
						"id": "checks", "title": "Launch checklist", "menu": "Checklist", "source": "/checks",
						"table": map[string]any{
							"rows": "checks", "key": "key",
							"status_links": map[string]any{"param": "show", "options": "views"},
							"columns": []map[string]any{
								{"key": "title", "label": "Item", "sub": "evidence", "link": map[string]any{"screen": "check", "params": map[string]string{"id": "key"}}},
								{"key": "state", "label": "State", "badge": true},
								{"key": "group_label", "label": "Area"},
								{"key": "launch_label", "label": "Blocks launch"},
							},
							"row_actions": []map[string]any{
								{"label": "Details", "screen": "check", "params": map[string]string{"id": "key"}},
							},
							"empty": map[string]any{"title": "Nothing to show", "text": "Every item in this view is done."},
						},
					},
					{
						"id": "check", "title": "Checklist item", "title_key": "title", "parent": "checks", "source": "/check",
						"detail": map[string]any{
							"record": "check",
							"main":   []map[string]any{{"title": "What it found", "items": "found"}},
							"side":   []map[string]any{{"title": "Item", "items": "about"}},
							"actions": []map[string]any{
								{"label": "Mark done", "post": "/manual", "params": map[string]string{"id": "key"}, "body": map[string]any{"done": true}, "show_if": map[string]any{"manual": true, "done": false}},
								{"label": "Mark not done", "post": "/manual", "params": map[string]string{"id": "key"}, "body": map[string]any{"done": false}, "show_if": map[string]any{"manual": true, "done": true}},
							},
						},
					},
				},
			}),
		},
		Handlers: map[string]plugin.Handler{"checks": listChecks, "check": showCheck, "badge": badge, "tick": tick},
		Default:  true,
	}
}

// tickRecord is a manual item's tick, kept under "manual:<key>".
type tickRecord struct {
	Done bool   `json:"done"`
	By   string `json:"by"`
	At   string `json:"at"`
	Note string `json:"note,omitempty"`
}

// all is every item: the checks (the site's own manual items among them),
// then the checklist's manual ones, each manual item with its tick.
func all(ctx context.Context, api *plugin.API) ([]plugin.Check, map[string]tickRecord, error) {
	checks, err := api.Checks(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, m := range manual {
		m.Source, m.Manual = "manual", true
		checks = append(checks, m)
	}
	ticks := map[string]tickRecord{}
	for i, c := range checks {
		if !c.Manual {
			continue
		}
		var t tickRecord
		if v, ok, _ := api.KVGet(ctx, "manual:"+c.Key); ok && json.Unmarshal([]byte(v), &t) == nil && t.Done {
			ticks[c.Key] = t
			checks[i].State, checks[i].Evidence = "done", fmt.Sprintf("Ticked by %s, %s.", t.By, t.At)
			if t.Note != "" {
				checks[i].Evidence += " " + t.Note
			}
		} else {
			checks[i].State = "todo"
			if checks[i].Evidence == "" || c.Source == "manual" {
				checks[i].Evidence = "Not ticked yet: a person checks this one."
			}
		}
	}
	slices.SortStableFunc(checks, func(a, b plugin.Check) int {
		if d := groupRank(a.Group) - groupRank(b.Group); d != 0 {
			return d
		}
		if a.Group != b.Group {
			return strings.Compare(a.Group, b.Group)
		}
		return boolRank(b.Launch) - boolRank(a.Launch)
	})
	return checks, ticks, nil
}

func groupRank(g string) int {
	if i := slices.Index(groupOrder, g); i >= 0 {
		return i
	}
	return len(groupOrder)
}

func boolRank(b bool) int {
	if b {
		return 1
	}
	return 0
}

// open is an item that isn't settled: failing, unknown, a warning, or a
// manual item not ticked.
func open(c plugin.Check) bool {
	return c.State == "fails" || c.State == "unknown" || c.State == "warns" || c.State == "todo"
}

// blocking is an item the site shouldn't go live with.
func blocking(c plugin.Check) bool {
	return c.Launch && (c.State == "fails" || c.State == "unknown" || c.State == "todo")
}

func listChecks(ctx context.Context, api *plugin.API, req *plugin.Request) (*plugin.Response, error) {
	checks, _, err := all(ctx, api)
	if err != nil {
		return nil, err
	}
	var blockers, toDo []plugin.Check
	for _, c := range checks {
		if blocking(c) {
			blockers = append(blockers, c)
		}
		if open(c) {
			toDo = append(toDo, c)
		}
	}
	// The verdict first: whether the site may go live.
	verdict := map[string]any{"key": "verdict", "group_label": "", "launch_label": "", "state": "ready",
		"title": "Ready to go live", "evidence": "Nothing that blocks launch is failing, unchecked or unticked."}
	if len(blockers) > 0 {
		titles := make([]string, len(blockers))
		for i, c := range blockers {
			titles[i] = c.Title
		}
		verdict["state"], verdict["title"] = "not ready", fmt.Sprintf("Not ready to go live: %d to do", len(blockers))
		verdict["evidence"] = strings.Join(titles, " · ")
	}
	show := req.Query.Get("show")
	rows := []map[string]any{verdict}
	for _, c := range checks {
		switch {
		case show == "" && !open(c), show == "blocking" && !blocking(c):
			continue
		}
		rows = append(rows, row(c))
	}
	views := []map[string]any{
		{"value": "", "label": "To do", "count": len(toDo)},
		{"value": "blocking", "label": "Blocks launch", "count": len(blockers)},
		{"value": "all", "label": "All", "count": len(checks)},
	}
	return plugin.JSON(http.StatusOK, map[string]any{"checks": rows, "views": views, "ready": len(blockers) == 0}), nil
}

var groupLabels = map[string]string{
	"general": "General", "email": "Email", "deploy": "Deploy", "tokens": "Tokens", "media": "Media", "content": "Content",
	"globals": "Globals", "forms": "Forms", "backup": "Backup", "manual": "Manual", "build": "Build", "site": "Site",
}

func row(c plugin.Check) map[string]any {
	group := groupLabels[c.Group]
	if group == "" {
		group = c.Group
	}
	launch := ""
	if c.Launch {
		launch = "Yes"
	}
	return map[string]any{"key": c.Key, "title": c.Title, "state": stateLabel(c.State), "evidence": c.Evidence,
		"group_label": group, "launch_label": launch}
}

// stateLabel is a state as the admin's badges read it.
func stateLabel(s string) string {
	switch s {
	case "n_a":
		return "not applicable"
	case "todo":
		return "to do"
	}
	return s
}

func showCheck(ctx context.Context, api *plugin.API, req *plugin.Request) (*plugin.Response, error) {
	checks, ticks, err := all(ctx, api)
	if err != nil {
		return nil, err
	}
	id := req.Query.Get("id")
	i := slices.IndexFunc(checks, func(c plugin.Check) bool { return c.Key == id })
	if i < 0 {
		return plugin.JSON(http.StatusNotFound, map[string]any{"error": "not_found", "message": "There's no checklist item " + id + "."}), nil
	}
	c := checks[i]
	found := []map[string]any{{"label": "State", "value": stateLabel(c.State), "badge": true}, {"label": "Evidence", "value": c.Evidence, "multiline": true}}
	if c.Fix != "" {
		found = append(found, map[string]any{"label": "Put it right in", "value": strings.TrimPrefix(c.FixLabel, ""), "href": c.Fix})
	}
	source := c.Source
	switch source {
	case "cms":
		source = "The CMS"
	case "manual":
		source = "A person"
	case "build":
		source = "The site's last build"
	default:
		source = "The " + strings.ToUpper(source[:1]) + source[1:] + " plugin"
	}
	launch := "No: advice"
	if c.Launch {
		launch = "Yes"
	}
	about := []map[string]any{{"label": "Checked by", "value": source}, {"label": "Blocks launch", "value": launch}, {"label": "Key", "value": c.Key}}
	_, done := ticks[c.Key]
	return plugin.JSON(http.StatusOK, map[string]any{"check": map[string]any{
		"key": c.Key, "title": c.Title, "found": found, "about": about, "manual": c.Manual, "done": done,
	}}), nil
}

func badge(ctx context.Context, api *plugin.API, _ *plugin.Request) (*plugin.Response, error) {
	checks, _, err := all(ctx, api)
	if err != nil {
		return nil, err
	}
	n := 0
	for _, c := range checks {
		if blocking(c) {
			n++
		}
	}
	return plugin.JSON(http.StatusOK, map[string]any{"count": n}), nil
}

func tick(ctx context.Context, api *plugin.API, req *plugin.Request) (*plugin.Response, error) {
	id := req.Query.Get("id")
	checks, _, err := all(ctx, api)
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(checks, func(c plugin.Check) bool { return c.Key == id && c.Manual }) {
		return plugin.JSON(http.StatusNotFound, map[string]any{"error": "not_found", "message": id + " isn't an item a person ticks."}), nil
	}
	var body struct {
		Done bool   `json:"done"`
		Note string `json:"note"`
	}
	if len(req.Body) > 0 {
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return plugin.JSON(http.StatusBadRequest, map[string]any{"error": "bad_request", "message": "Send {done, note}."}), nil //nolint:nilerr // answered as a 400
		}
	}
	if !body.Done {
		if err := api.KVDelete(ctx, "manual:"+id); err != nil {
			return nil, err
		}
		return plugin.JSON(http.StatusOK, map[string]any{"ok": true, "message": "Marked not done."}), nil
	}
	by := "someone"
	if req.Actor != nil && req.Actor.Name != "" {
		by = req.Actor.Name
	}
	rec, _ := json.Marshal(tickRecord{Done: true, By: by, At: time.Now().UTC().Format("2 Jan 2006 15:04 UTC"), Note: strings.TrimSpace(body.Note)})
	if err := api.KVSet(ctx, "manual:"+id, string(rec)); err != nil {
		return nil, err
	}
	return plugin.JSON(http.StatusOK, map[string]any{"ok": true, "message": "Marked done."}), nil
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

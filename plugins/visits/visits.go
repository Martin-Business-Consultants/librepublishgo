// Package visits is an example Libre Publish plugin. It counts the page
// views a site reports, and remembers when each page was last published. It
// asks for its own storage and to read content — nothing else — and can't
// reach anything it didn't ask for.
package visits

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/plugin"
)

// Module is the plugin, for the CMS to register.
func Module() *plugin.Module {
	return &plugin.Module{
		Manifest: plugin.Manifest{
			Key:         "visits",
			Name:        "Visits",
			Version:     "1.0.0",
			Description: "Counts the page views a site reports, and when each page was last published.",
			Permissions: plugin.Grants{Storage: true, ContentRead: true},
			Hooks:       []string{"page.published"},
			Routes: []plugin.Route{
				{Method: "POST", Path: "/hit", Export: "hit", Public: true},
				{Method: "GET", Path: "/stats", Export: "stats"},
			},
			Admin: mustJSON(map[string]any{"screens": []map[string]any{{
				"id": "stats", "title": "Visits", "source": "/stats",
				"table": map[string]any{"rows": "pages", "columns": []map[string]string{
					{"key": "path", "label": "Page"}, {"key": "views", "label": "Views"}, {"key": "published_at", "label": "Last published"},
				}},
			}}}),
		},
		Handlers: map[string]plugin.Handler{"hit": hit, "stats": stats},
		OnEvent:  onEvent,
	}
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// hit counts one view of a page that exists and is live.
func hit(ctx context.Context, api *plugin.API, req *plugin.Request) (*plugin.Response, error) {
	var body struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(req.Body, &body); err != nil {
		// The caller's mistake, answered; not the plugin failing.
		return plugin.JSON(http.StatusBadRequest, map[string]string{"error": "bad_request"}), nil //nolint:nilerr // answered as a 400
	}
	path := strings.Trim(body.Path, "/")
	if path == "" {
		path = "home"
	}
	if _, err := api.Content(ctx, plugin.ContentQuery{Page: path}); err != nil {
		// No such live page (or content:read taken back): nothing to count.
		api.Log(err.Error())
		return plugin.JSON(http.StatusNotFound, map[string]string{"error": "not_found"}), nil
	}
	n, _, err := api.KVGet(ctx, "views:"+path)
	if err != nil {
		// Without storage, count from nothing.
		api.Log(err.Error())
	}
	count, _ := strconv.Atoi(n)
	if err := api.KVSet(ctx, "views:"+path, strconv.Itoa(count+1)); err != nil {
		api.Log(err.Error())
	}
	return plugin.JSON(http.StatusOK, map[string]any{"path": path, "views": count + 1}), nil
}

func stats(ctx context.Context, api *plugin.API, _ *plugin.Request) (*plugin.Response, error) {
	keys, err := api.KVAll(ctx, "views:")
	if err != nil {
		return nil, err
	}
	pages := []map[string]any{}
	for _, k := range keys {
		path := strings.TrimPrefix(k, "views:")
		v, _, _ := api.KVGet(ctx, k)
		n, _ := strconv.Atoi(v)
		at, _, _ := api.KVGet(ctx, "published:"+path)
		pages = append(pages, map[string]any{"path": path, "views": n, "published_at": at})
	}
	return plugin.JSON(http.StatusOK, map[string]any{"pages": pages}), nil
}

// onEvent hears what it hooked: page.published.
func onEvent(ctx context.Context, api *plugin.API, _ string, data json.RawMessage) error {
	var page struct {
		Path        string `json:"path"`
		PublishedAt string `json:"published_at"`
	}
	if err := json.Unmarshal(data, &page); err != nil {
		return err
	}
	api.Log("published " + page.Path)
	return api.KVSet(ctx, "published:"+page.Path, page.PublishedAt)
}

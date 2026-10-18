package cms

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/mail"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/plugin"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

// pluginEnv is what plugins reach of the CMS (plugin.Env).
type pluginEnv struct{ a *App }

// Setting is the settings key's data.
func (e pluginEnv) Setting(ctx context.Context, key string) (map[string]any, error) {
	return e.a.setting(ctx, key)
}

// ReplaceSetting writes data as the settings key, whole.
func (e pluginEnv) ReplaceSetting(ctx context.Context, key string, data map[string]any) error {
	_, err := e.a.putSetting(ctx, nil, key, data, true)
	return err
}

// Site is the site plugins run on.
func (e pluginEnv) Site(ctx context.Context) (plugin.Site, error) {
	g, err := e.a.setting(ctx, "general")
	if err != nil {
		return plugin.Site{}, err
	}
	email, err := e.a.setting(ctx, "email")
	if err != nil {
		return plugin.Site{}, err
	}
	return plugin.Site{Name: str(g["title"]), URL: strings.TrimRight(str(g["site_base_url"]), "/"), Locale: e.a.defaultLocale(), CMSURL: e.a.BaseURL(),
		ContactEmail: strOr(str(g["email"]), strOr(str(email["reply_to"]), str(email["from_email"]))), Origins: e.a.siteOrigins(ctx)}, nil
}

// SendMail sends m through the site's mail settings.
func (e pluginEnv) SendMail(ctx context.Context, m mail.Message) error { return e.a.sendMail(ctx, m) }

// PutMedia adds f to the media library.
func (e pluginEnv) PutMedia(ctx context.Context, f plugin.Media) (map[string]any, error) {
	as, err := e.a.uploadAsset(ctx, nil, f.Filename, f.ContentType, bytes.NewReader(f.Data), map[string]string{"folder": f.Folder, "alt": f.Alt})
	if err != nil {
		return nil, err
	}
	var out map[string]any
	err = json.Unmarshal(store.MustJSON(e.a.assetJSON(ctx, as)), &out)
	return out, err
}

// Audit records a plugin's action in the audit log.
func (e pluginEnv) Audit(ctx context.Context, actor plugin.Actor, action string, id int64, label string, meta map[string]any) {
	who, _ := actor.(*Actor)
	e.a.audit(ctx, who, action, "Plugin", id, label, meta)
}

// Content answers a plugin's query for live content, as the delivery API
// shows it.
func (e pluginEnv) Content(ctx context.Context, q plugin.ContentQuery) (any, error) {
	a := e.a
	switch {
	case q.Page != "":
		p, err := a.pageByPath(ctx, q.Page, true)
		if err != nil {
			return nil, err
		}
		return a.v1PageJSON(ctx, p)
	case q.Pages:
		list, err := a.pages(ctx, "status = 'published' ORDER BY path LIMIT 500")
		if err != nil {
			return nil, err
		}
		out := []Obj{}
		for _, p := range list {
			out = append(out, O("id", p.ID, "path", p.Path, "title", p.Title, "locale", p.Locale, "url", a.publicPath(p), "updated_at", p.UpdatedAt))
		}
		return out, nil
	case q.Collection != "":
		col, err := a.collectionBySlug(ctx, q.Collection)
		if err != nil {
			return nil, err
		}
		if q.Entry != "" {
			e, err := a.entryBySlug(ctx, col.ID, q.Entry, true)
			if err != nil {
				return nil, err
			}
			return a.v1EntryJSON(ctx, e)
		}
		list, err := a.entries(ctx, "e.collection_id = ? AND e.status = 'published' ORDER BY e.published_at DESC LIMIT 500", col.ID)
		if err != nil {
			return nil, err
		}
		out := []Obj{}
		for _, e := range list {
			o, err := a.v1EntryJSON(ctx, e)
			if err != nil {
				return nil, err
			}
			out = append(out, o)
		}
		return out, nil
	case q.Global != "":
		g, err := a.globalBySlug(ctx, q.Global)
		if err != nil {
			return nil, err
		}
		return a.v1GlobalJSON(ctx, g), nil
	}
	return nil, fmt.Errorf("ask for a page, the pages, a collection (or one of its entries) or a global")
}

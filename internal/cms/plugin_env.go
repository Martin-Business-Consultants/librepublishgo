package cms

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/mail"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/plugin"
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

// Site is lp_site's answer.
func (e pluginEnv) Site(ctx context.Context) (map[string]any, error) {
	g, err := e.a.setting(ctx, "general")
	if err != nil {
		return nil, err
	}
	email, err := e.a.setting(ctx, "email")
	if err != nil {
		return nil, err
	}
	return map[string]any{"name": str(g["title"]), "url": strings.TrimRight(str(g["site_base_url"]), "/"), "locale": e.a.defaultLocale(), "cms_url": e.a.BaseURL(), "contact_email": strOr(str(g["email"]), strOr(str(email["reply_to"]), str(email["from_email"]))),
		"origins": e.a.siteOrigins(ctx)}, nil
}

// SendMail sends m through the site's mail settings.
func (e pluginEnv) SendMail(ctx context.Context, m mail.Message) error { return e.a.sendMail(ctx, m) }

// PutMedia adds f to the media library.
func (e pluginEnv) PutMedia(ctx context.Context, f plugin.Media) (any, error) {
	as, err := e.a.uploadAsset(ctx, nil, f.Filename, f.ContentType, bytes.NewReader(f.Data), map[string]string{"folder": f.Folder, "alt": f.Alt})
	if err != nil {
		return nil, err
	}
	return e.a.assetJSON(ctx, as), nil
}

// Audit records a plugin's action in the audit log.
func (e pluginEnv) Audit(ctx context.Context, actor plugin.Actor, action string, id int64, label string, meta map[string]any) {
	who, _ := actor.(*Actor)
	e.a.audit(ctx, who, action, "Plugin", id, label, meta)
}

// Content answers lp_content: {"page": "about"}, {"pages": true},
// {"collection": "posts"}, {"collection": "posts", "entry": "hello"},
// {"global": "nav"} — live content only, as the delivery API shows it.
func (e pluginEnv) Content(ctx context.Context, in string) (any, error) {
	var q map[string]any
	if err := json.Unmarshal([]byte(in), &q); err != nil {
		return nil, fmt.Errorf("lp_content takes a JSON query")
	}
	a := e.a
	switch {
	case q["page"] != nil:
		p, err := a.pageByPath(ctx, str(q["page"]), true)
		if err != nil {
			return nil, err
		}
		return a.v1PageJSON(ctx, p)
	case q["pages"] != nil:
		list, err := a.pages(ctx, "status = 'published' ORDER BY path LIMIT 500")
		if err != nil {
			return nil, err
		}
		out := []Obj{}
		for _, p := range list {
			out = append(out, O("id", p.ID, "path", p.Path, "title", p.Title, "locale", p.Locale, "url", a.publicPath(p), "updated_at", p.UpdatedAt))
		}
		return out, nil
	case q["collection"] != nil:
		col, err := a.collectionBySlug(ctx, str(q["collection"]))
		if err != nil {
			return nil, err
		}
		if q["entry"] != nil {
			e, err := a.entryBySlug(ctx, col.ID, str(q["entry"]), true)
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
	case q["global"] != nil:
		g, err := a.globalBySlug(ctx, str(q["global"]))
		if err != nil {
			return nil, err
		}
		return a.v1GlobalJSON(ctx, g), nil
	}
	return nil, fmt.Errorf(`lp_content: ask for {"page"}, {"pages"}, {"collection"[, "entry"]} or {"global"}`)
}

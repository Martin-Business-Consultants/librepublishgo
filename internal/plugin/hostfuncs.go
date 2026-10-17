package plugin

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	extism "github.com/extism/go-sdk"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/mail"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

func (h *Host) hostFunctions(key string, grants Grants) []extism.HostFunction {
	ptr := []extism.ValueType{extism.ValueTypePTR}
	// fn wraps a host function: input string in, output string out, errors
	// as {"error": "…"} so a plugin can't crash the host.
	fn := func(name string, allowed bool, impl func(ctx context.Context, in string) (any, error)) extism.HostFunction {
		return extism.NewHostFunctionWithStack(name, func(ctx context.Context, p *extism.CurrentPlugin, stack []uint64) {
			in, err := p.ReadString(stack[0])
			var out any
			switch {
			case err != nil:
				out = map[string]any{"error": "couldn't read the input"}
			case !allowed:
				out = map[string]any{"error": name + " wasn't granted to this plugin"}
			default:
				out, err = impl(ctx, in)
				if err != nil {
					out = map[string]any{"error": err.Error()}
				}
			}
			b, _ := json.Marshal(out)
			offs, err := p.WriteBytes(b)
			if err != nil {
				stack[0] = 0
				return
			}
			stack[0] = offs
		}, ptr, ptr)
	}
	return []extism.HostFunction{
		fn("lp_log", true, func(ctx context.Context, in string) (any, error) {
			h.log.Info("plugin", "plugin", key, "message", truncate(in, 2000))
			return map[string]any{"ok": true}, nil
		}),
		fn("lp_site", true, func(ctx context.Context, in string) (any, error) { return h.env.Site(ctx) }),
		fn("lp_mail", grants.Mail, func(ctx context.Context, in string) (any, error) {
			var m mail.Message
			if err := json.Unmarshal([]byte(in), &m); err != nil {
				return nil, fmt.Errorf(`lp_mail takes {"to": […], "subject": "…", "text": "…"}`)
			}
			if !h.mail.allow(key) {
				return nil, fmt.Errorf("this plugin has sent 200 emails in the last hour; try later")
			}
			if err := h.env.SendMail(ctx, m); err != nil {
				return nil, err
			}
			h.log.Info("plugin mail", "plugin", key, "to", len(m.To)+len(m.Cc)+len(m.Bcc), "subject", truncate(m.Subject, 120))
			return map[string]any{"ok": true}, nil
		}),
		fn("lp_media_put", grants.MediaWrite, func(ctx context.Context, in string) (any, error) {
			var f struct {
				Filename    string `json:"filename"`
				ContentType string `json:"content_type"`
				Data        string `json:"data"`
				Folder      string `json:"folder"`
				Alt         string `json:"alt"`
			}
			if err := json.Unmarshal([]byte(in), &f); err != nil || f.Filename == "" || f.Data == "" {
				return nil, fmt.Errorf(`lp_media_put takes {"filename": "…", "data": "<base64>", "content_type", "folder", "alt"}`)
			}
			data, err := base64.StdEncoding.DecodeString(f.Data)
			if err != nil {
				return nil, fmt.Errorf("data must be base64")
			}
			if len(data) > maxBody {
				return nil, fmt.Errorf("a file from a plugin is at most 10 MB")
			}
			if f.Folder == "" {
				f.Folder = "Plugins/" + key
			}
			return h.env.PutMedia(ctx, Media{Filename: f.Filename, ContentType: f.ContentType, Data: data, Folder: f.Folder, Alt: f.Alt})
		}),
		fn("lp_content", grants.ContentRead, func(ctx context.Context, in string) (any, error) { return h.env.Content(ctx, in) }),
		fn("lp_kv_get", grants.Storage, func(ctx context.Context, in string) (any, error) {
			var v []byte
			err := h.db.QueryRowContext(ctx, `SELECT value FROM plugin_kv WHERE plugin_key = ? AND key = ?`, key, in).Scan(&v)
			if errors.Is(err, sql.ErrNoRows) {
				return map[string]any{"found": false}, nil
			}
			return map[string]any{"found": err == nil, "value": string(v)}, err
		}),
		fn("lp_kv_set", grants.Storage, func(ctx context.Context, in string) (any, error) {
			var kvIn struct {
				Key   string `json:"key"`
				Value string `json:"value"`
			}
			if err := json.Unmarshal([]byte(in), &kvIn); err != nil || kvIn.Key == "" {
				return nil, fmt.Errorf(`lp_kv_set takes {"key": "…", "value": "…"}`)
			}
			if len(kvIn.Value) > 1<<20 {
				return nil, fmt.Errorf("a value is at most 1 MB")
			}
			n, err := store.Count(ctx, h.db, `SELECT count(*) FROM plugin_kv WHERE plugin_key = ?`, key)
			if err != nil {
				return nil, err
			}
			if n > 100000 {
				return nil, fmt.Errorf("this plugin's storage is full (100,000 keys)")
			}
			_, err = h.db.ExecContext(ctx, `INSERT INTO plugin_kv (plugin_key, key, value, updated_at) VALUES (?,?,?,?)
				ON CONFLICT(plugin_key, key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, key, kvIn.Key, []byte(kvIn.Value), store.Now().DB())
			return map[string]any{"ok": err == nil}, err
		}),
		fn("lp_kv_delete", grants.Storage, func(ctx context.Context, in string) (any, error) {
			_, err := h.db.ExecContext(ctx, `DELETE FROM plugin_kv WHERE plugin_key = ? AND key = ?`, key, in)
			return map[string]any{"ok": err == nil}, err
		}),
		// lp_kv_list takes a prefix, or {"prefix", "after", "limit"} to page
		// (keys in order, the next page's "after" in "next"), or {"prefix",
		// "count": true} for how many there are.
		fn("lp_kv_list", grants.Storage, func(ctx context.Context, in string) (any, error) {
			q := struct {
				Prefix string `json:"prefix"`
				After  string `json:"after"`
				Limit  int    `json:"limit"`
				Count  bool   `json:"count"`
			}{Prefix: in}
			if strings.HasPrefix(in, "{") {
				if err := json.Unmarshal([]byte(in), &q); err != nil {
					return nil, fmt.Errorf(`lp_kv_list takes a prefix or {"prefix", "after", "limit", "count"}`)
				}
			}
			like := strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(q.Prefix) + "%"
			if q.Count {
				var n int
				err := h.db.QueryRowContext(ctx, `SELECT count(*) FROM plugin_kv WHERE plugin_key = ? AND key LIKE ? ESCAPE '\'`, key, like).Scan(&n)
				return map[string]any{"count": n}, err
			}
			if q.Limit <= 0 || q.Limit > 1000 {
				q.Limit = 1000
			}
			rows, err := h.db.QueryContext(ctx, `SELECT key FROM plugin_kv WHERE plugin_key = ? AND key LIKE ? ESCAPE '\' AND key > ? ORDER BY key LIMIT ?`, key, like, q.After, q.Limit+1)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			keys, err := store.ScanAll(rows, func(r *sql.Rows) (string, error) {
				var k string
				err := r.Scan(&k)
				return k, err
			})
			if err != nil {
				return nil, err
			}
			if keys == nil {
				keys = []string{}
			}
			out := map[string]any{"keys": keys}
			if len(keys) > q.Limit {
				out["keys"], out["next"] = keys[:q.Limit], keys[q.Limit-1]
			}
			return out, nil
		}),
	}
}

// mailLimiter caps what each plugin sends: 200 messages an hour.
type mailLimiter struct {
	mu    sync.Mutex
	sends map[string][]time.Time
}

func (l *mailLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.sends == nil {
		l.sends = map[string][]time.Time{}
	}
	cut := time.Now().Add(-time.Hour)
	kept := l.sends[key][:0]
	for _, t := range l.sends[key] {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= 200 {
		l.sends[key] = kept
		return false
	}
	l.sends[key] = append(kept, time.Now())
	return true
}

package cms

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"strings"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/apierr"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

// setting reads one settings key's data ({} when it isn't set). Any other
// failure is an error, never {}: a write merging into {} would wipe the key.
func (a *App) setting(ctx context.Context, key string) (map[string]any, error) {
	var data store.JSON
	err := a.store.QueryRowContext(ctx, `SELECT data FROM settings WHERE key = ?`, key).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("setting %s: %w", key, err)
	}
	m := map[string]any{}
	if len(data) > 0 && string(data) != "null" {
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("setting %s: %w", key, err)
		}
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

// settingOr is setting for what only reads (a page's title, CORS origins):
// a failure is logged and reads as {}.
func (a *App) settingOr(ctx context.Context, key string) map[string]any {
	m, err := a.setting(ctx, key)
	if err != nil {
		a.log.Error("setting", "key", key, "err", err)
		return map[string]any{}
	}
	return m
}

func (a *App) general(ctx context.Context) map[string]any { return a.settingOr(ctx, "general") }

func (a *App) defaultLocale() string {
	if l := str(a.general(context.Background())["default_locale"]); l != "" {
		return l
	}
	return "en"
}

// putSetting merges data into a key (null removes a key), keeping what a
// masked secret written back would otherwise overwrite. The branding is
// checked and replaced whole (cleanBranding).
func (a *App) putSetting(ctx context.Context, actor *Actor, key string, data map[string]any, replace bool) (map[string]any, error) {
	if key == brandingKey {
		clean, err := a.cleanBranding(ctx, data)
		if err != nil {
			return nil, err
		}
		data, replace = clean, true
	}
	old, err := a.setting(ctx, key)
	if err != nil {
		return nil, err
	}
	merged := map[string]any{}
	if !replace {
		maps.Copy(merged, old)
	}
	keepMasked(merged, old, data)
	if prepare := a.beforeSetting[key]; prepare != nil {
		prepare(merged)
	}
	if key == "general" {
		u, err := cleanCMSURL(str(merged["cms_url"]))
		if err != nil {
			return nil, err
		}
		if u == "" {
			delete(merged, "cms_url")
		} else {
			merged["cms_url"] = u
		}
	}
	now := store.Now().DB()
	_, err = a.store.ExecContext(ctx, `INSERT INTO settings (key, data, created_at, updated_at) VALUES (?,?,?,?)
		ON CONFLICT(key) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`, key, string(store.MustJSON(merged)), now, now)
	if err != nil {
		return nil, err
	}
	a.audit(ctx, actor, "setting.updated", "Setting", 0, key, map[string]any{"keys": keysOf(data)})
	if key == "general" {
		a.setCMSURL(str(merged["cms_url"]))
	}
	if key == "general" || key == brandingKey {
		a.publish(ctx, change{Tags: []string{"site"}})
	}
	return merged, nil
}

// setCMSURL keeps Settings › General's CMS address for BaseURL.
func (a *App) setCMSURL(u string) {
	u = strings.TrimRight(strings.TrimSpace(u), "/")
	a.cmsURL.Store(&u)
}

// cleanCMSURL is the CMS address a person typed, as the base URLs start
// with: an absolute http(s) address, no query or fragment, no trailing
// slash; "" for none.
func cleanCMSURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		inv := apierr.Invalid{}
		inv.Add("cms_url", "must be this CMS's address, like https://cms.example.com")
		return "", inv.Err()
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func keepMasked(into, old, data map[string]any) {
	for k, v := range data {
		switch x := v.(type) {
		case nil:
			delete(into, k)
		case string:
			if strings.HasPrefix(x, "***") && secretKey(k) {
				if ov, ok := old[k]; ok {
					into[k] = ov
				}
				continue
			}
			into[k] = x
		case map[string]any:
			sub, _ := old[k].(map[string]any)
			if sub == nil {
				sub = map[string]any{}
			}
			next := map[string]any{}
			maps.Copy(next, sub)
			keepMasked(next, sub, x)
			into[k] = next
		default:
			into[k] = v
		}
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

var secretPattern = regexp.MustCompile(`(?i)(token|secret|password|api_key|apikey|private_key|access_key|credential)`)

func secretKey(k string) bool { return secretPattern.MatchString(k) }

// redact masks secret-looking values: "***" and the last four characters.
func redact(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, val := range x {
			if s, ok := val.(string); ok && secretKey(k) && s != "" {
				tail := s
				if len(s) > 4 {
					tail = s[len(s)-4:]
				}
				out[k] = "***" + tail
			} else {
				out[k] = redact(val)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = redact(val)
		}
		return out
	}
	return v
}

// deploySettings is the deploy's view of the settings: what it records, it
// writes as the system.
type deploySettings struct{ a *App }

// Setting is the settings key's data.
func (s deploySettings) Setting(ctx context.Context, key string) (map[string]any, error) {
	return s.a.setting(ctx, key)
}

// ReplaceSetting writes data as the settings key, whole.
func (s deploySettings) ReplaceSetting(ctx context.Context, key string, data map[string]any) error {
	_, err := s.a.putSetting(ctx, nil, key, data, true)
	return err
}

// Package deploy rebuilds the site after content changes: a build hook for
// a static site, a signed purge for one rendered on demand.
package deploy

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/jsonv"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/outbound"
)

// Settings is where the deploy reads settings/deploy and settings/frontend,
// and records how each deploy went (deploy_status).
type Settings interface {
	Setting(ctx context.Context, key string) (map[string]any, error)
	// ReplaceSetting writes data as key's, replacing what was there.
	ReplaceSetting(ctx context.Context, key string, data map[string]any) error
}

// Spawn runs fn in the background for as long as the app lives; false when
// it's closed (fn doesn't run).
type Spawn func(ctx context.Context, name string, fn func(context.Context)) bool

// Debouncer rebuilds the site after content changes. Changes inside the
// debounce window go out as one.
type Debouncer struct {
	settings Settings
	log      *slog.Logger
	spawn    Spawn
	mu       sync.Mutex
	tags     map[string]bool
	timer    *time.Timer
	stopped  bool
}

// New is a debouncer configured by settings, deploying in the background
// with spawn.
func New(settings Settings, log *slog.Logger, spawn Spawn) *Debouncer {
	return &Debouncer{settings: settings, log: log, spawn: spawn}
}

// settingOr is Setting for what only reads: a failure is logged and reads
// as {}.
func (d *Debouncer) settingOr(ctx context.Context, key string) map[string]any {
	m, err := d.settings.Setting(ctx, key)
	if err != nil {
		d.log.Error("setting", "key", key, "err", err)
		return map[string]any{}
	}
	return m
}

// maxDebounce caps settings/deploy's debounce_seconds (a day), well short
// of where seconds overflow a Duration.
const maxDebounce = 24 * 60 * 60

// Changed notes what a publish touched (cache tags) and schedules the
// rebuild or purge.
func (d *Debouncer) Changed(ctx context.Context, tags ...string) {
	cfg := d.settingOr(ctx, "deploy")
	if jsonv.String(cfg["hook_url"]) == "" && jsonv.String(d.settingOr(ctx, "frontend")["webhook_url"]) == "" {
		return
	}
	if v, ok := cfg["enabled"]; ok && !jsonv.Bool(v) {
		return
	}
	delay := 30 * time.Second
	if s, ok := jsonv.Int(cfg["debounce_seconds"]); ok && s >= 0 {
		delay = time.Duration(min(s, maxDebounce)) * time.Second
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped {
		return
	}
	if d.tags == nil {
		d.tags = map[string]bool{}
	}
	for _, t := range tags {
		d.tags[t] = true
	}
	if d.timer != nil {
		d.timer.Stop()
	}
	d.timer = time.AfterFunc(delay, func() {
		d.spawn(ctx, "deploy", func(ctx context.Context) {
			if _, err := d.Trigger(ctx, "publish"); err != nil {
				d.log.Warn("deploy", "err", err)
			}
		})
	})
}

// Stop cancels a deploy that's waiting out its debounce, and any later one.
func (d *Debouncer) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stopped = true
	if d.timer != nil {
		d.timer.Stop()
	}
}

// Trigger runs the deploy now and records how it went.
func (d *Debouncer) Trigger(ctx context.Context, reason string) (map[string]any, error) {
	d.mu.Lock()
	tags := make([]string, 0, len(d.tags))
	for t := range d.tags {
		tags = append(tags, t)
	}
	d.tags = map[string]bool{}
	d.mu.Unlock()
	slices.Sort(tags)

	cfg, err := d.settings.Setting(ctx, "deploy")
	if err != nil {
		return nil, err
	}
	front, err := d.settings.Setting(ctx, "frontend")
	if err != nil {
		return nil, err
	}
	status := map[string]any{"at": time.Now().UTC().Format(time.RFC3339), "reason": reason, "tags": tags}

	// On demand (server): purge what changed, signed with the frontend's
	// secret. Static: rebuild through the build hook. Hybrid (static pages
	// and on-demand ones): both.
	render, purgeURL, hookURL := jsonv.String(front["render"]), jsonv.String(front["webhook_url"]), jsonv.String(cfg["hook_url"])
	purge := (render == "server" || render == "hybrid") && purgeURL != ""
	build := hookURL != "" && (render != "server" || purgeURL == "")
	purgeIt := func() error {
		return d.post(ctx, purgeURL, jsonv.String(front["secret"]), map[string]any{"tags": tags, "reason": reason})
	}
	buildIt := func() error {
		return d.post(ctx, hookURL, "", map[string]any{"tags": tags, "reason": reason, "source": "librepublish"})
	}
	switch {
	case purge && build:
		status["kind"] = "purge+build"
		err = errors.Join(purgeIt(), buildIt())
	case purge:
		status["kind"] = "purge"
		err = purgeIt()
	case build:
		status["kind"] = "build"
		err = buildIt()
	default:
		status["status"] = "disabled"
		return status, d.settings.ReplaceSetting(ctx, "deploy_status", status)
	}
	if err != nil {
		status["status"], status["error"] = "failure", err.Error()
	} else {
		status["status"] = "success"
	}
	if perr := d.settings.ReplaceSetting(ctx, "deploy_status", status); perr != nil {
		return status, errors.Join(err, perr)
	}
	return status, err
}

func (d *Debouncer) post(ctx context.Context, url, secret string, payload any) error {
	if err := outbound.CheckURL(url); err != nil {
		return err
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		req.Header.Set("X-CMS-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	resp, err := outbound.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("the hook answered HTTP %d", resp.StatusCode)
	}
	return nil
}

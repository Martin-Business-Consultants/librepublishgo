package cms

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/apierr"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

// scheduler publishes and unpublishes what's due, and empties old trash.
func (a *App) scheduler(ctx context.Context) {
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		a.runSchedule(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// scheduleFailed logs a scheduled save that didn't happen. A conflict is
// another process's scheduler (two run while a deploy overlaps them)
// saving it first: nothing's wrong, so it's only a debug line.
func (a *App) scheduleFailed(what string, c *Content, err error) {
	if e, ok := errors.AsType[*apierr.Error](err); ok && e.Status == http.StatusConflict {
		a.log.Debug(what, "record", c.Label(), "err", err)
		return
	}
	a.log.Warn(what, "record", c.Label(), "err", err)
}

var systemActor = &Actor{Name: "Scheduler", Role: &Role{System: true}}

func (a *App) runSchedule(ctx context.Context) {
	now := store.Now().DB()
	for _, kind := range []string{"page", "entry"} {
		var due, gone []*Content
		var err error
		if kind == "page" {
			due, err = a.pages(ctx, "publish_at IS NOT NULL AND publish_at <= ? AND status != 'published'", now)
			if err == nil {
				gone, err = a.pages(ctx, "unpublish_at IS NOT NULL AND unpublish_at <= ? AND status = 'published'", now)
			}
		} else {
			due, err = a.entries(ctx, "e.publish_at IS NOT NULL AND e.publish_at <= ? AND e.status != 'published'", now)
			if err == nil {
				gone, err = a.entries(ctx, "e.unpublish_at IS NOT NULL AND e.unpublish_at <= ? AND e.status = 'published'", now)
			}
		}
		if err != nil {
			a.log.Warn("scheduler", "err", err)
			continue
		}
		for _, c := range due {
			if _, err := a.saveContent(ctx, systemActor, c, map[string]any{"status": "published"}); err != nil {
				a.scheduleFailed("scheduled publish", c, err)
			}
		}
		for _, c := range gone {
			if _, err := a.saveContent(ctx, systemActor, c, map[string]any{"status": "draft", "unpublish_at": nil}); err != nil {
				a.scheduleFailed("scheduled unpublish", c, err)
			}
		}
	}
	if _, err := a.store.ExecContext(ctx, `DELETE FROM device_authorizations WHERE expires_at < ?`, store.Now().DB()); err != nil {
		a.log.Warn("expired device codes", "err", err)
	}
	if _, err := a.store.ExecContext(ctx, `DELETE FROM webhook_deliveries WHERE created_at < ?`, store.TimeOf(time.Now().Add(-30*24*time.Hour)).DB()); err != nil {
		a.log.Warn("old webhook deliveries", "err", err)
	}
}

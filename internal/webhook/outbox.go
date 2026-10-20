package webhook

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/outbound"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

// workers is how many deliveries are sent at once.
const workers = 4

// Backoff is how long after each failed attempt the next one is made: six
// attempts over about an hour, and then the delivery is given up on.
var Backoff = []time.Duration{30 * time.Second, 2 * time.Minute, 5 * time.Minute, 15 * time.Minute, 40 * time.Minute}

// lease is how long a delivery being sent is kept from the other workers,
// and, after a crash mid-send, how soon it's tried again.
const lease = 2 * time.Minute

// outbox is the Dispatcher's queue: rows in webhook_outbox, sent by a few
// workers, which start with the first Deliver or Start.
type outbox struct {
	wake    chan struct{} // a delivery was queued
	start   sync.Once
	running atomic.Int32 // workers running
	busy    atomic.Int32 // workers claiming or sending, for Wait
}

// Deliver queues event for every active webhook that takes it (its events
// and filters), to be sent in the background: the request that caused it
// can end first, and a delivery that fails is tried again.
func (d *Dispatcher) Deliver(ctx context.Context, event string, data any) {
	ctx = context.WithoutCancel(ctx) // queued even when the request has gone
	hooks, err := d.List(ctx, "active = 1")
	if err != nil {
		d.log.Warn("webhooks", "err", err)
		return
	}
	eventData, err := json.Marshal(data)
	if err != nil {
		d.log.Warn("webhooks", "event", event, "err", err)
		return
	}
	queued := 0
	for _, h := range hooks {
		if !h.Takes(event, data) {
			continue
		}
		now := store.Now().DB()
		if _, err := d.db.ExecContext(ctx, `INSERT INTO webhook_outbox (webhook_id, delivery_id, event, event_data, next_attempt_at, created_at) VALUES (?,?,?,?,?,?)`,
			h.ID, outbound.NewDeliveryID(), event, string(eventData), now, now); err != nil {
			d.log.Warn("webhook outbox", "webhook", h.ID, "err", err)
			continue
		}
		queued++
	}
	if queued > 0 {
		d.Start(ctx)
		for range min(queued, workers) {
			select {
			case d.wake <- struct{}{}:
			default:
			}
		}
	}
}

// Start runs the workers, which send what's queued, what an earlier run
// left included. Calling it again does nothing.
func (d *Dispatcher) Start(ctx context.Context) {
	d.start.Do(func() {
		for range workers {
			d.running.Add(1)
			if !d.spawn(ctx, "webhook worker", func(ctx context.Context) {
				defer d.running.Add(-1)
				d.work(ctx)
			}) {
				d.running.Add(-1)
			}
		}
	})
}

// Wait is for tests: nothing queued is due, and no worker is sending. A
// delivery waiting to be tried again later doesn't count.
func (d *Dispatcher) Wait() {
	for d.running.Load() > 0 {
		due, err := store.Count(context.Background(), d.db, `SELECT count(*) FROM webhook_outbox WHERE next_attempt_at <= ?`, store.Now().DB())
		if err != nil || due == 0 && d.busy.Load() == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (d *Dispatcher) work(ctx context.Context) {
	for ctx.Err() == nil {
		if d.sendNext(ctx) {
			continue
		}
		select {
		case <-ctx.Done():
		case <-d.wake:
		case <-time.After(d.untilNext(ctx)):
		}
	}
}

// untilNext is how long until the next delivery is due: at most a minute,
// since one queued wakes a worker anyway.
func (d *Dispatcher) untilNext(ctx context.Context) time.Duration {
	var next store.Time
	if err := d.db.QueryRowContext(ctx, `SELECT MIN(next_attempt_at) FROM webhook_outbox`).Scan(&next); err != nil || !next.Valid {
		return time.Minute
	}
	return min(max(time.Until(next.Time), 10*time.Millisecond), time.Minute)
}

// queued is one row of the outbox.
type queued struct {
	id, webhookID int64
	deliveryID    string
	event         string
	data          json.RawMessage
	attempts      int // this one included
}

// sendNext sends the delivery due first, if one is: false when none is.
func (d *Dispatcher) sendNext(ctx context.Context) bool {
	d.busy.Add(1)
	defer d.busy.Add(-1)
	q, err := d.claim(ctx)
	if err != nil {
		if ctx.Err() == nil {
			d.log.Warn("webhook outbox", "err", err)
		}
		return false
	}
	if q == nil {
		return false
	}
	d.attempt(ctx, q)
	return true
}

// claim takes the delivery due first, leasing it (its next attempt moved
// on by lease) and counting the attempt.
func (d *Dispatcher) claim(ctx context.Context) (*queued, error) {
	var q *queued
	err := d.db.Tx(ctx, func(tx *sql.Tx) error {
		r := &queued{}
		var data sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT id, webhook_id, delivery_id, event, event_data, attempts FROM webhook_outbox WHERE next_attempt_at <= ? ORDER BY next_attempt_at, id LIMIT 1`, store.Now().DB()).
			Scan(&r.id, &r.webhookID, &r.deliveryID, &r.event, &data, &r.attempts)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		r.attempts++
		r.data = json.RawMessage("null")
		if data.Valid && json.Valid([]byte(data.String)) {
			r.data = json.RawMessage(data.String)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE webhook_outbox SET attempts = ?, next_attempt_at = ? WHERE id = ?`, r.attempts, store.TimeOf(time.Now().Add(lease)).DB(), r.id); err != nil {
			return err
		}
		q = r
		return nil
	})
	return q, err
}

// attempt sends q and settles it: done (delivered, given up on, or its
// webhook gone or off), or due again after its backoff. One cut off by the
// app closing is put back as it was, for the next start to send.
func (d *Dispatcher) attempt(ctx context.Context, q *queued) {
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	hooks, err := d.List(bg, "id = ?", q.webhookID)
	if err != nil {
		d.putBack(bg, q, err)
		return
	}
	if len(hooks) == 0 || !hooks[0].Active {
		d.done(bg, q)
		return
	}
	h := hooks[0]
	a := d.post(ctx, h, q.event, q.data, h.URL, q.deliveryID)
	if !a.ok() && ctx.Err() != nil {
		d.putBack(bg, q, nil)
		return
	}
	d.record(bg, h, q.event, q.data, false, a)
	switch {
	case a.ok():
		d.done(bg, q)
	case !a.retry() || q.attempts > len(Backoff):
		d.log.Warn("webhook delivery given up", "webhook", h.ID, "event", q.event, "delivery", q.deliveryID, "attempts", q.attempts)
		d.done(bg, q)
	default:
		why := a.err
		if why == nil {
			why = fmt.Errorf("HTTP %d", a.status)
		}
		d.retryAt(bg, q, time.Now().Add(Backoff[q.attempts-1]), why)
	}
}

func (d *Dispatcher) done(ctx context.Context, q *queued) {
	if _, err := d.db.ExecContext(ctx, `DELETE FROM webhook_outbox WHERE id = ?`, q.id); err != nil {
		d.log.Warn("webhook outbox", "delivery", q.deliveryID, "err", err)
	}
}

func (d *Dispatcher) retryAt(ctx context.Context, q *queued, at time.Time, why error) {
	if _, err := d.db.ExecContext(ctx, `UPDATE webhook_outbox SET next_attempt_at = ?, last_error = ? WHERE id = ?`, store.TimeOf(at).DB(), why.Error(), q.id); err != nil {
		d.log.Warn("webhook outbox", "delivery", q.deliveryID, "err", err)
	}
}

// putBack undoes claim: the attempt wasn't made (or was cut off), so it
// doesn't count, and the delivery is due now.
func (d *Dispatcher) putBack(ctx context.Context, q *queued, why error) {
	if why != nil {
		d.log.Warn("webhook outbox", "delivery", q.deliveryID, "err", why)
	}
	if _, err := d.db.ExecContext(ctx, `UPDATE webhook_outbox SET attempts = ?, next_attempt_at = ? WHERE id = ?`, q.attempts-1, store.Now().DB(), q.id); err != nil {
		d.log.Warn("webhook outbox", "delivery", q.deliveryID, "err", err)
	}
}

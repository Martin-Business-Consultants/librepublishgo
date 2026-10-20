// Package ratelimit counts requests per key (a token, an address) in
// one-minute windows.
package ratelimit

import (
	"cmp"
	"fmt"
	"net/netip"
	"slices"
	"sync"
	"time"
)

// Limiter counts requests per key in one-minute windows.
type Limiter struct {
	mu        sync.Mutex
	limit     int
	window    map[string]*bucket
	lastPrune time.Time
}

type bucket struct {
	start time.Time
	count int
}

// maxRateKeys caps how many callers a limiter remembers at once.
const maxRateKeys = 100000

// New is a limiter allowing limit requests a minute per key; 0 or less
// is no limit.
func New(limit int) *Limiter {
	return &Limiter{limit: limit, window: map[string]*bucket{}, lastPrune: time.Now()}
}

// Describe is the limit, in words.
func (l *Limiter) Describe() string {
	if l.limit <= 0 {
		return "none"
	}
	return fmt.Sprintf("%d a minute", l.limit)
}

// Allow counts one request for key; the seconds to wait when it's over.
func (l *Limiter) Allow(key string) (bool, int) {
	if l.limit <= 0 {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b := l.window[key]
	if b == nil || now.Sub(b.start) >= time.Minute {
		if b == nil {
			l.prune(now)
		}
		b = &bucket{start: now}
		l.window[key] = b
	}
	b.count++
	if b.count > l.limit {
		return false, int(time.Minute.Seconds()-now.Sub(b.start).Seconds()) + 1
	}
	return true, 0
}

// prune forgets expired windows, at most once a minute unless the cap is
// reached; when that still leaves over half the cap (a flood of new
// addresses) it forgets the oldest windows down to half, which only ever
// lets someone through early, and never the ones a flood just opened.
func (l *Limiter) prune(now time.Time) {
	if len(l.window) < 10000 || now.Sub(l.lastPrune) < time.Minute && len(l.window) < maxRateKeys {
		return
	}
	l.lastPrune = now
	for k, v := range l.window {
		if now.Sub(v.start) >= time.Minute {
			delete(l.window, k)
		}
	}
	if len(l.window) <= maxRateKeys/2 {
		return
	}
	type aged struct {
		key   string
		start time.Time
	}
	all := make([]aged, 0, len(l.window))
	for k, v := range l.window {
		all = append(all, aged{k, v.start})
	}
	slices.SortFunc(all, func(a, b aged) int { return a.start.Compare(b.start) })
	for _, e := range all[:len(all)-maxRateKeys/2] {
		delete(l.window, e.key)
	}
}

// IPKey is the address a limit counts a client by: an IPv4 address as it
// is, an IPv6 one by its /64 (one subscriber's, which hands out the rest
// freely). What isn't an address is used as given.
func IPKey(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	addr = addr.Unmap()
	if addr.Is4() {
		return addr.String()
	}
	p, _ := addr.WithZone("").Prefix(64)
	return cmp.Or(p.String(), ip)
}

package ratelimit

import (
	"fmt"
	"testing"
	"testing/synctest"
	"time"
)

func TestLimiterStaysBounded(t *testing.T) {
	t.Parallel()
	l := New(1)
	for i := range maxRateKeys + 10 {
		l.Allow(fmt.Sprint("k", i))
	}
	if n := len(l.window); n > maxRateKeys {
		t.Fatalf("%d keys remembered, cap %d", n, maxRateKeys)
	}
	if ok, _ := l.Allow("again"); !ok {
		t.Fatal("a new caller is let through")
	}
	if ok, _ := l.Allow("again"); ok {
		t.Fatal("its second request in the minute isn't")
	}
}

func TestLimiterWindows(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		l := New(2)
		allow := func(key string, want bool, wantWait int) {
			t.Helper()
			ok, wait := l.Allow(key)
			if ok != want || wait != wantWait {
				t.Fatalf("Allow(%q) = %v, %d; want %v, %d", key, ok, wait, want, wantWait)
			}
		}
		allow("a", true, 0)
		allow("a", true, 0)
		allow("a", false, 61)
		allow("b", true, 0) // keys are counted apart
		time.Sleep(30 * time.Second)
		allow("a", false, 31)
		time.Sleep(30 * time.Second)
		allow("a", true, 0) // a new minute
	})
}

func TestNoLimit(t *testing.T) {
	t.Parallel()
	for _, limit := range []int{0, -1} {
		l := New(limit)
		for range 1000 {
			if ok, _ := l.Allow("k"); !ok {
				t.Fatalf("New(%d) refused a request", limit)
			}
		}
		if d := l.Describe(); d != "none" {
			t.Errorf("New(%d).Describe() = %q", limit, d)
		}
	}
	if d := New(30).Describe(); d != "30 a minute" {
		t.Errorf("Describe() = %q", d)
	}
}

func TestFloodEvictsOldestOnly(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		l := New(1)
		l.Allow("target")
		time.Sleep(time.Second)
		// A flood of new keys after it: the oldest go, not every one.
		for i := range maxRateKeys {
			l.Allow(fmt.Sprint("k", i))
			if i%1000 == 0 {
				time.Sleep(time.Millisecond)
			}
		}
		if n := len(l.window); n > maxRateKeys || n < maxRateKeys/2 {
			t.Fatalf("%d keys remembered after the flood", n)
		}
		if l.window["target"] != nil {
			t.Fatal("the oldest key outlived the flood")
		}
		if ok, _ := l.Allow(fmt.Sprint("k", maxRateKeys-1)); ok {
			t.Fatal("a key the flood just opened was forgotten")
		}
	})
}

func TestIPKey(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"203.0.113.9":                   "203.0.113.9",
		"::ffff:203.0.113.9":            "203.0.113.9",
		"2001:db8:1:2:3:4:5:6":          "2001:db8:1:2::/64",
		"2001:db8:1:2:ffff:ffff:ffff:1": "2001:db8:1:2::/64",
		"fe80::1%eth0":                  "fe80::/64",
		"not an address":                "not an address",
	} {
		if got := IPKey(in); got != want {
			t.Errorf("IPKey(%q) = %q, want %q", in, got, want)
		}
	}
}

package qos

import (
	"net/http"
	"testing"
	"time"
)

func TestBucketAllowsBurstThenRejects(t *testing.T) {
	start := time.Now()
	b := newBucket(3, time.Second, start)

	for i := 0; i < 3; i++ {
		if !b.allow(start) {
			t.Fatalf("request %d should fit in burst", i)
		}
	}
	if b.allow(start) {
		t.Fatal("4th request should be rejected")
	}
}

func TestBucketRefillsOverTime(t *testing.T) {
	start := time.Now()
	b := newBucket(2, time.Second, start)

	b.allow(start)
	b.allow(start)

	// Half a second later one of two tokens has refilled.
	if !b.allow(start.Add(500 * time.Millisecond)) {
		t.Fatal("refilled token should be available")
	}
	if b.allow(start.Add(500 * time.Millisecond)) {
		t.Fatal("only one token should have refilled")
	}
}

func TestRateLimiterKeysIndependent(t *testing.T) {
	start := time.Now()
	l := NewRateLimiter(1, time.Minute)

	if !l.AllowAt("a", start) {
		t.Fatal("first request for key a should pass")
	}
	if l.AllowAt("a", start) {
		t.Fatal("second request for key a should be limited")
	}
	if !l.AllowAt("b", start) {
		t.Fatal("key b has its own bucket")
	}
}

func TestRateLimiterSweepsIdleKeys(t *testing.T) {
	start := time.Now()
	l := NewRateLimiter(1, time.Second)
	l.AllowAt("a", start)

	l.sweep(start.Add(2 * time.Second))

	l.mu.Lock()
	n := len(l.buckets)
	l.mu.Unlock()
	if n != 0 {
		t.Errorf("idle bucket not swept, %d remain", n)
	}
}

func TestCachePutGetExpiry(t *testing.T) {
	start := time.Now()
	c := NewCache()

	c.Put("k", 200, http.Header{"X-Test": {"v"}}, []byte("body"), time.Minute, start)

	got := c.Get("k", start.Add(30*time.Second))
	if got == nil {
		t.Fatal("entry should still be fresh")
	}
	if got.Status != 200 || string(got.Body) != "body" || got.Header.Get("X-Test") != "v" {
		t.Errorf("cached entry = %+v", got)
	}

	if c.Get("k", start.Add(61*time.Second)) != nil {
		t.Error("entry should be expired")
	}
}

func TestCacheHeaderClone(t *testing.T) {
	start := time.Now()
	c := NewCache()
	h := http.Header{}
	h.Set("X-Test", "original")

	c.Put("k", 200, h, nil, time.Minute, start)
	h.Set("X-Test", "mutated")

	if got := c.Get("k", start).Header.Get("X-Test"); got != "original" {
		t.Errorf("header not cloned, got %q", got)
	}
}

func TestCacheZeroTTLNoop(t *testing.T) {
	c := NewCache()
	now := time.Now()
	c.Put("k", 200, nil, []byte("x"), 0, now)
	if c.Get("k", now) != nil {
		t.Error("ttl 0 must not store anything")
	}
}

func TestBreakerTripsAfterConsecutiveFailures(t *testing.T) {
	start := time.Now()
	b := NewBreaker(3, 30*time.Second)

	for i := 0; i < 2; i++ {
		if err := b.AllowAt(start); err != nil {
			t.Fatalf("closed breaker must allow: %v", err)
		}
		b.RecordFailureAt(start)
	}
	if b.State() != "closed" {
		t.Fatalf("state = %s after 2/3 failures, want closed", b.State())
	}

	b.RecordFailureAt(start)
	if b.State() != "open" {
		t.Fatalf("state = %s after 3 failures, want open", b.State())
	}
	if err := b.AllowAt(start); err != ErrBreakerOpen {
		t.Fatalf("open breaker must reject, got %v", err)
	}
}

func TestBreakerSuccessResetsFailures(t *testing.T) {
	b := NewBreaker(3, 30*time.Second)
	b.RecordFailure()
	b.RecordFailure()
	b.RecordSuccess()
	b.RecordFailure()
	b.RecordFailure()

	if b.State() != "closed" {
		t.Errorf("state = %s, want closed (success reset count)", b.State())
	}
}

func TestBreakerHalfOpenProbe(t *testing.T) {
	start := time.Now()
	b := NewBreaker(1, 30*time.Second)

	b.RecordFailureAt(start) // trips with maxFailures=1
	if b.State() != "open" {
		t.Fatalf("state = %s, want open", b.State())
	}

	// Still within open timeout: rejected.
	if err := b.AllowAt(start.Add(time.Second)); err != ErrBreakerOpen {
		t.Fatal("breaker should still be open")
	}

	// After the timeout the first caller becomes the probe.
	if err := b.AllowAt(start.Add(31 * time.Second)); err != nil {
		t.Fatalf("half-open probe should be allowed: %v", err)
	}

	// Successful probe closes the breaker.
	b.RecordSuccess()
	if b.State() != "closed" {
		t.Errorf("state = %s after successful probe, want closed", b.State())
	}
}

func TestBreakerFailedProbeReopens(t *testing.T) {
	start := time.Now()
	b := NewBreaker(1, 30*time.Second)

	b.RecordFailureAt(start)
	if err := b.AllowAt(start.Add(31 * time.Second)); err != nil {
		t.Fatalf("probe should be allowed: %v", err)
	}
	b.RecordFailureAt(start.Add(31 * time.Second))

	if b.State() != "open" {
		t.Errorf("state = %s after failed probe, want open", b.State())
	}
	if err := b.AllowAt(start.Add(32 * time.Second)); err != ErrBreakerOpen {
		t.Fatal("failed probe must re-open breaker")
	}
}

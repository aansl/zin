package qos

import (
	"math/rand"
	"sync"
	"time"
)

// bucket is a token bucket allowing `burst` requests instantly and
// refilling `rate` tokens per second.
type bucket struct {
	mu       sync.Mutex
	tokens   float64
	last     time.Time
	lastUsed time.Time
	rate     float64
	burst    float64
}

func newBucket(max int, per time.Duration, now time.Time) *bucket {
	rate := float64(max) / per.Seconds()
	return &bucket{
		tokens:   float64(max),
		last:     now,
		lastUsed: now,
		rate:     rate,
		burst:    float64(max),
	}
}

func (b *bucket) allow(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tokens = min(b.burst, b.tokens+now.Sub(b.last).Seconds()*b.rate)
	b.last = now
	b.lastUsed = now
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// idleFor reports how long the bucket has gone unused, for sweeping.
func (b *bucket) idleFor(now time.Time) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	return now.Sub(b.lastUsed)
}

// RateLimiter is a registry of token buckets sharing the same capacity,
// keyed by caller-chosen identity (route + client key). It enforces
// `max` requests per `per` window per key.
type RateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	max     int
	per     time.Duration
}

func NewRateLimiter(max int, per time.Duration) *RateLimiter {
	return &RateLimiter{
		buckets: make(map[string]*bucket),
		max:     max,
		per:     per,
	}
}

// Allow consumes one token for key, reporting whether the request fits.
func (l *RateLimiter) Allow(key string) bool {
	return l.AllowAt(key, time.Now())
}

func (l *RateLimiter) AllowAt(key string, now time.Time) bool {
	l.mu.Lock()
	b, ok := l.buckets[key]
	if !ok {
		b = newBucket(l.max, l.per, now)
		l.buckets[key] = b
	}
	l.mu.Unlock()

	if allowed := b.allow(now); !allowed {
		return false
	}

	// Keyed buckets (per-IP, per-header) can grow without bound; sweep
	// idle ones occasionally so the map stays bounded by active clients.
	if rand.Intn(512) == 0 {
		l.sweep(now)
	}
	return true
}

// sweep removes buckets idle for longer than one refill window.
func (l *RateLimiter) sweep(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, b := range l.buckets {
		if b.idleFor(now) > l.per {
			delete(l.buckets, k)
		}
	}
}

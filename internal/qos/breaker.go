package qos

import (
	"errors"
	"sync"
	"time"
)

// ErrBreakerOpen is returned by Allow while the breaker is open.
var ErrBreakerOpen = errors.New("circuit breaker open")

type breakerState int

const (
	stateClosed breakerState = iota
	stateOpen
	stateHalfOpen
)

// Breaker is a per-backend circuit breaker: it opens after maxFailures
// consecutive failures, stays open for openTimeout, then lets a single
// half-open probe through. A successful probe closes it again.
type Breaker struct {
	mu          sync.Mutex
	maxFailures int
	openTimeout time.Duration

	state    breakerState
	failures int
	openedAt time.Time
}

func NewBreaker(maxFailures int, openTimeout time.Duration) *Breaker {
	return &Breaker{
		maxFailures: maxFailures,
		openTimeout: openTimeout,
	}
}

// Allow reports whether a request may proceed. In half-open state only
// the single in-flight probe is allowed.
func (b *Breaker) Allow() error {
	return b.AllowAt(time.Now())
}

func (b *Breaker) AllowAt(now time.Time) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case stateOpen:
		if now.Sub(b.openedAt) >= b.openTimeout {
			b.state = stateHalfOpen
			return nil // this call is the probe
		}
		return ErrBreakerOpen
	default: // closed or half-open
		return nil
	}
}

// RecordSuccess closes the breaker and resets the failure count.
func (b *Breaker) RecordSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.state = stateClosed
	b.failures = 0
}

// RecordFailure counts a failure and may trip the breaker open.
func (b *Breaker) RecordFailure() {
	b.RecordFailureAt(time.Now())
}

func (b *Breaker) RecordFailureAt(now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case stateHalfOpen:
		b.state = stateOpen
		b.openedAt = now
	case stateClosed:
		b.failures++
		if b.failures >= b.maxFailures {
			b.state = stateOpen
			b.openedAt = now
		}
	}
}

// State exposes the current state for logging and tests.
func (b *Breaker) State() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case stateOpen:
		return "open"
	case stateHalfOpen:
		return "half-open"
	default:
		return "closed"
	}
}

// Package config defines the gateway configuration schema and loads it
// from disk. A configuration is either a single JSON file or a directory
// whose config.json may pull in additional backend files via "include"
// globs.
package config

import (
	"encoding/json"
	"fmt"
	"time"
)

// Duration wraps time.Duration so JSON configs can use "60s", "5m", etc.
type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("duration must be a string like \"60s\" or \"5m\": %w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	d.Duration = v
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.String())
}

// Config is the root of the gateway configuration.
type Config struct {
	Listen   string    `json:"listen"`
	Include  []string  `json:"include"`
	Backends []Backend `json:"backends"`
}

// Backend is one upstream service and the routes it serves.
type Backend struct {
	Name     string         `json:"name"`
	URL      string         `json:"url"`
	Breaker  *BreakerConfig `json:"breaker"`
	Defaults *RouteDefaults `json:"defaults"`
	Routes   []Route        `json:"routes"`
}

// RouteDefaults are inherited by every route of a backend unless the
// route sets its own value.
type RouteDefaults struct {
	Timeout   *Duration        `json:"timeout"`
	Cache     *CacheConfig     `json:"cache"`
	RateLimit *RateLimitConfig `json:"rate_limit"`
	Headers   *HeadersConfig   `json:"headers"`
}

// Route is a single public endpoint pattern.
type Route struct {
	Method    string           `json:"method"`
	Pattern   string           `json:"pattern"`
	Target    string           `json:"target"`
	Timeout   *Duration        `json:"timeout"`
	Cache     *CacheConfig     `json:"cache"`
	RateLimit *RateLimitConfig `json:"rate_limit"`
	Headers   *HeadersConfig   `json:"headers"`
	Static    *StaticConfig    `json:"static"`

	// Params are the path parameters extracted from Pattern at load time.
	Params []string `json:"-"`
}

// CacheConfig enables response caching for a route. A TTL of zero
// explicitly disables caching (override of a backend default).
type CacheConfig struct {
	TTL Duration `json:"ttl"`
}

// RateLimitConfig is a token bucket: Max requests per Per window,
// burst equals Max. Key selects who shares the bucket.
type RateLimitConfig struct {
	Max int      `json:"max"`
	Per Duration `json:"per"`
	Key string   `json:"key"` // "global" (default), "ip", "header:NAME"
}

// HeadersConfig holds manipulation rules applied to the proxied request
// and to the response returned to the client.
type HeadersConfig struct {
	Request  *HeaderRules `json:"request"`
	Response *HeaderRules `json:"response"`
}

type HeaderRules struct {
	Set    map[string]string `json:"set"`
	Add    map[string]string `json:"add"`
	Remove []string          `json:"remove"`
}

// StaticConfig makes the gateway answer directly without an upstream call.
type StaticConfig struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
}

// BreakerConfig configures the per-backend circuit breaker.
type BreakerConfig struct {
	MaxFailures int      `json:"max_failures"`
	OpenTimeout Duration `json:"open_timeout"`
}

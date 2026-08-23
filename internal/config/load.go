package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
)

// Load reads a configuration from path. path may be a directory
// (its config.json is the top file) or a single JSON file. It returns
// the parsed config and the directory watches should observe.
func Load(path string) (*Config, string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, "", fmt.Errorf("config path %q: %w", path, err)
	}

	var topFile, rootDir string
	if info.IsDir() {
		topFile = filepath.Join(path, "config.json")
		rootDir = path
	} else {
		topFile = path
		rootDir = filepath.Dir(path)
	}

	cfg, err := loadFile(topFile)
	if err != nil {
		return nil, "", err
	}

	// Expand include globs (relative to rootDir) into backends. Includes
	// are appended after inline backends and replace them on name conflict.
	byName := make(map[string]int)
	for i, be := range cfg.Backends {
		byName[be.Name] = i
	}
	for _, glob := range cfg.Include {
		matches, err := filepath.Glob(filepath.Join(rootDir, glob))
		if err != nil {
			return nil, "", fmt.Errorf("include %q: %w", glob, err)
		}
		for _, m := range matches {
			be, err := loadBackendFile(m)
			if err != nil {
				return nil, "", err
			}
			if i, ok := byName[be.Name]; ok {
				cfg.Backends[i] = *be
			} else {
				byName[be.Name] = len(cfg.Backends)
				cfg.Backends = append(cfg.Backends, *be)
			}
		}
	}
	cfg.Include = nil

	applyDefaults(cfg)
	if err := validate(cfg); err != nil {
		return nil, "", err
	}
	return cfg, rootDir, nil
}

func loadFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := unmarshalStrict(data, &cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &cfg, nil
}

func loadBackendFile(path string) (*Backend, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read backend file: %w", err)
	}
	var be Backend
	if err := unmarshalStrict(data, &be); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &be, nil
}

func unmarshalStrict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// applyDefaults propagates backend defaults into routes that do not set
// their own value.
func applyDefaults(cfg *Config) {
	for i := range cfg.Backends {
		be := &cfg.Backends[i]
		d := be.Defaults
		for j := range be.Routes {
			r := &be.Routes[j]
			r.Params = ExtractParams(r.Pattern)
			if d == nil {
				continue
			}
			if r.Timeout == nil {
				r.Timeout = d.Timeout
			}
			if r.Cache == nil {
				r.Cache = d.Cache
			}
			if r.RateLimit == nil {
				r.RateLimit = d.RateLimit
			}
			if r.Headers == nil {
				r.Headers = d.Headers
			}
		}
	}
}

func validate(cfg *Config) error {
	seen := make(map[string]bool)
	for i := range cfg.Backends {
		be := &cfg.Backends[i]
		if be.Name == "" {
			return fmt.Errorf("backend %d: name is required", i)
		}
		u, err := url.Parse(be.URL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("backend %q: invalid url %q", be.Name, be.URL)
		}
		if be.Breaker != nil {
			if be.Breaker.MaxFailures <= 0 {
				return fmt.Errorf("backend %q: breaker.max_failures must be > 0", be.Name)
			}
			if be.Breaker.OpenTimeout.Duration <= 0 {
				return fmt.Errorf("backend %q: breaker.open_timeout must be > 0", be.Name)
			}
		}
		for j := range be.Routes {
			r := &be.Routes[j]
			if err := validateRoute(be.Name, r); err != nil {
				return err
			}
			key := r.Method + " " + r.Pattern
			if seen[key] {
				return fmt.Errorf("route %q registered by more than one backend", key)
			}
			seen[key] = true
		}
	}
	return nil
}

func validateRoute(backend string, r *Route) error {
	where := fmt.Sprintf("backend %q route %q", backend, r.Pattern)
	if r.Pattern == "" || r.Pattern[0] != '/' {
		return fmt.Errorf("%s: pattern must start with /", where)
	}
	if r.Static == nil {
		if r.Target != "" && r.Target[0] != '/' {
			return fmt.Errorf("%s: target must start with /", where)
		}
	}
	if r.RateLimit != nil {
		if r.RateLimit.Max <= 0 {
			return fmt.Errorf("%s: rate_limit.max must be > 0", where)
		}
		if r.RateLimit.Per.Duration <= 0 {
			return fmt.Errorf("%s: rate_limit.per must be > 0", where)
		}
		switch {
		case r.RateLimit.Key == "", r.RateLimit.Key == "global", r.RateLimit.Key == "ip":
		case len(r.RateLimit.Key) > 7 && r.RateLimit.Key[:7] == "header:":
			if r.RateLimit.Key[7:] == "" {
				return fmt.Errorf("%s: rate_limit.key header name is empty", where)
			}
		default:
			return fmt.Errorf("%s: rate_limit.key must be \"global\", \"ip\" or \"header:NAME\"", where)
		}
	}
	if r.Cache != nil && r.Cache.TTL.Duration < 0 {
		return fmt.Errorf("%s: cache.ttl must be >= 0", where)
	}
	if r.Static != nil && (r.Static.Status < 100 || r.Static.Status > 599) {
		return fmt.Errorf("%s: static.status must be a valid HTTP status", where)
	}
	return nil
}

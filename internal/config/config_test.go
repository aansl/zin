package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestLoadSingleFile(t *testing.T) {
	root := writeTree(t, map[string]string{
		"config.json": `{
			"listen": ":9090",
			"backends": [{
				"name": "api",
				"url": "http://localhost:3002",
				"routes": [{ "method": "GET", "pattern": "/items" }]
			}]
		}`,
	})

	cfg, _, err := Load(filepath.Join(root, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != ":9090" {
		t.Errorf("listen = %q", cfg.Listen)
	}
	if len(cfg.Backends) != 1 || cfg.Backends[0].Name != "api" {
		t.Errorf("backends = %+v", cfg.Backends)
	}
	if got := cfg.Backends[0].Routes[0].Params; len(got) != 0 {
		t.Errorf("params = %v", got)
	}
}

func TestLoadDirWithIncludes(t *testing.T) {
	root := writeTree(t, map[string]string{
		"config.json": `{
			"listen": ":8081",
			"include": ["backends/*.json"],
			"backends": [{ "name": "inline", "url": "http://localhost:1", "routes": [{"pattern": "/inline"}] }]
		}`,
		"backends/auth.json": `{
			"name": "auth",
			"url": "http://localhost:3001",
			"routes": [{ "method": "GET", "pattern": "/user/{id}" }]
		}`,
		"backends/api.json": `{
			"name": "api",
			"url": "http://localhost:3002",
			"routes": [{ "method": "GET", "pattern": "/items/{id}/detail" }]
		}`,
		"backends/notes.txt": "ignored by glob",
	})

	cfg, dir, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if dir != root {
		t.Errorf("root dir = %q", dir)
	}
	if len(cfg.Backends) != 3 {
		t.Fatalf("backends = %d, want 3", len(cfg.Backends))
	}
	route := cfg.Backends[2].Routes[0]
	want := []string{"id"}
	if len(route.Params) != 1 || route.Params[0] != "id" {
		t.Errorf("params = %v, want %v", route.Params, want)
	}
}

func TestIncludeReplacesInlineOnNameConflict(t *testing.T) {
	root := writeTree(t, map[string]string{
		"config.json": `{
			"include": ["b.json"],
			"backends": [{ "name": "api", "url": "http://inline:1", "routes": [{"pattern": "/a"}] }]
		}`,
		"b.json": `{ "name": "api", "url": "http://included:2", "routes": [{"pattern": "/b"}] }`,
	})

	cfg, _, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Backends) != 1 {
		t.Fatalf("backends = %d, want 1", len(cfg.Backends))
	}
	if cfg.Backends[0].URL != "http://included:2" {
		t.Errorf("url = %q, want included", cfg.Backends[0].URL)
	}
}

func TestDefaultsMerge(t *testing.T) {
	root := writeTree(t, map[string]string{
		"config.json": `{
			"backends": [{
				"name": "api",
				"url": "http://localhost:3002",
				"defaults": {
					"timeout": "10s",
					"rate_limit": { "max": 100, "per": "1m" }
				},
				"routes": [
					{ "pattern": "/inherit" },
					{ "pattern": "/override", "rate_limit": { "max": 2, "per": "1s", "key": "ip" } }
				]
			}]
		}`,
	})

	cfg, _, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	routes := cfg.Backends[0].Routes

	if routes[0].Timeout == nil || routes[0].Timeout.Duration != 10*time.Second {
		t.Errorf("inherited timeout = %+v", routes[0].Timeout)
	}
	if routes[0].RateLimit == nil || routes[0].RateLimit.Max != 100 {
		t.Errorf("inherited rate_limit = %+v", routes[0].RateLimit)
	}

	if routes[1].RateLimit.Max != 2 || routes[1].RateLimit.Key != "ip" {
		t.Errorf("route rate_limit override = %+v", routes[1].RateLimit)
	}
	if routes[1].Timeout == nil || routes[1].Timeout.Duration != 10*time.Second {
		t.Errorf("timeout should still inherit = %+v", routes[1].Timeout)
	}
}

func TestUnknownFieldRejected(t *testing.T) {
	root := writeTree(t, map[string]string{
		"config.json": `{
			"backends": [{ "name": "a", "url": "http://x:1", "routes": [{ "pattrn": "/x" }] }]
		}`,
	})

	_, _, err := Load(root)
	if err == nil {
		t.Fatal("want error for unknown field, got nil")
	}
	if !strings.Contains(err.Error(), "pattrn") {
		t.Errorf("error should name the unknown field: %v", err)
	}
}

func TestDuplicateRouteRejected(t *testing.T) {
	root := writeTree(t, map[string]string{
		"config.json": `{
			"backends": [
				{ "name": "a", "url": "http://x:1", "routes": [{ "method": "GET", "pattern": "/x" }] },
				{ "name": "b", "url": "http://x:2", "routes": [{ "method": "GET", "pattern": "/x" }] }
			]
		}`,
	})

	_, _, err := Load(root)
	if err == nil || !strings.Contains(err.Error(), "more than one backend") {
		t.Fatalf("want duplicate route error, got %v", err)
	}
}

func TestInvalidDurationRejected(t *testing.T) {
	root := writeTree(t, map[string]string{
		"config.json": `{
			"backends": [{ "name": "a", "url": "http://x:1", "routes": [{ "pattern": "/x", "timeout": "fast" }] }]
		}`,
	})

	_, _, err := Load(root)
	if err == nil {
		t.Fatal("want error for invalid duration")
	}
}

func TestInvalidRateLimitKeyRejected(t *testing.T) {
	root := writeTree(t, map[string]string{
		"config.json": `{
			"backends": [{ "name": "a", "url": "http://x:1", "routes": [{ "pattern": "/x", "rate_limit": { "max": 1, "per": "1s", "key": "cookie" } }] }]
		}`,
	})

	_, _, err := Load(root)
	if err == nil || !strings.Contains(err.Error(), "rate_limit.key") {
		t.Fatalf("want rate_limit.key error, got %v", err)
	}
}

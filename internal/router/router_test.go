package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aansl/zin/internal/config"
)

// upstream is a test backend that counts every request and lets each
// path be programmed with a handler.
type upstream struct {
	mu       sync.Mutex
	counts   map[string]int
	handlers map[string]http.HandlerFunc
	srv      *httptest.Server
}

func newUpstream(t *testing.T) *upstream {
	u := &upstream{
		counts:   make(map[string]int),
		handlers: make(map[string]http.HandlerFunc),
	}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.counts[r.URL.Path]++
		h := u.handlers[r.URL.Path]
		u.mu.Unlock()
		if h == nil {
			h = func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }
		}
		h(w, r)
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *upstream) on(path string, h http.HandlerFunc) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.handlers[path] = h
}

func (u *upstream) count(path string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.counts[path]
}

func (u *upstream) url() string { return u.srv.URL }

func d(s string) config.Duration {
	var dd config.Duration
	if err := dd.UnmarshalJSON([]byte(`"` + s + `"`)); err != nil {
		panic(err)
	}
	return dd
}

func routeCfg(method, pattern string, mutate func(*config.Route)) config.Route {
	r := config.Route{Method: method, Pattern: pattern}
	r.Params = config.ExtractParams(pattern)
	if mutate != nil {
		mutate(&r)
	}
	return r
}

func buildGateway(t *testing.T, be *config.Backend) http.Handler {
	t.Helper()
	cfg := &config.Config{Listen: ":0", Backends: []config.Backend{*be}}
	for i := range cfg.Backends {
		for j := range cfg.Backends[i].Routes {
			cfg.Backends[i].Routes[j].Params = config.ExtractParams(cfg.Backends[i].Routes[j].Pattern)
		}
	}
	mux, err := Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return mux
}

func TestCacheHitAvoidsUpstream(t *testing.T) {
	up := newUpstream(t)
	var hits atomic.Int32
	up.on("/items", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, "payload")
	})

	be := &config.Backend{
		Name: "api",
		URL:  up.url(),
		Routes: []config.Route{
			routeCfg("GET", "/items", func(r *config.Route) {
				r.Cache = &config.CacheConfig{TTL: d("1h")}
			}),
		},
	}
	gw := buildGateway(t, be)

	resp, _ := http.Get(serveURL(t, gw) + "/items")
	resp.Body.Close()
	if resp.Header.Get("X-Cache") != "" {
		t.Errorf("first response should be a miss, got X-Cache=%q", resp.Header.Get("X-Cache"))
	}

	resp2, _ := http.Get(serveURL(t, gw) + "/items")
	resp2.Body.Close()
	if resp2.Header.Get("X-Cache") != "HIT" {
		t.Errorf("second response should be HIT, got %q", resp2.Header.Get("X-Cache"))
	}
	if hits.Load() != 1 {
		t.Errorf("upstream hit %d times, want 1", hits.Load())
	}

	// Distinct query string = distinct cache key.
	resp3, _ := http.Get(serveURL(t, gw) + "/items?page=2")
	resp3.Body.Close()
	if hits.Load() != 2 {
		t.Errorf("query variant should miss cache, upstream hit %d", hits.Load())
	}
}

func TestRateLimitRejectsBurst(t *testing.T) {
	up := newUpstream(t)
	be := &config.Backend{
		Name: "api",
		URL:  up.url(),
		Routes: []config.Route{
			routeCfg("GET", "/limited", func(r *config.Route) {
				r.RateLimit = &config.RateLimitConfig{Max: 2, Per: d("1m"), Key: "global"}
			}),
		},
	}
	gw := buildGateway(t, be)
	url := serveURL(t, gw) + "/limited"

	for i := 0; i < 2; i++ {
		resp, err := http.Get(url)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d = %d, want 200", i, resp.StatusCode)
		}
	}

	resp, _ := http.Get(url)
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("third request = %d, want 429", resp.StatusCode)
	}
	if up.count("/limited") != 2 {
		t.Errorf("upstream saw %d requests, want 2", up.count("/limited"))
	}
}

func TestTimeoutReturns504(t *testing.T) {
	up := newUpstream(t)
	up.on("/slow", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(200)
	})

	be := &config.Backend{
		Name: "api",
		URL:  up.url(),
		Routes: []config.Route{
			routeCfg("GET", "/slow", func(r *config.Route) {
				r.Timeout = &config.Duration{}
				*r.Timeout = d("100ms")
			}),
		},
	}
	gw := buildGateway(t, be)

	resp, err := http.Get(serveURL(t, gw) + "/slow")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Errorf("status = %d, want 504", resp.StatusCode)
	}
}

func TestBreakerOpensAndRejects(t *testing.T) {
	up := newUpstream(t)
	var served atomic.Int32
	up.on("/flaky", func(w http.ResponseWriter, r *http.Request) {
		served.Add(1)
		w.WriteHeader(500)
	})

	be := &config.Backend{
		Name:    "api",
		URL:     up.url(),
		Breaker: &config.BreakerConfig{MaxFailures: 2, OpenTimeout: d("1m")},
		Routes: []config.Route{
			routeCfg("GET", "/flaky", nil),
		},
	}
	gw := buildGateway(t, be)
	url := serveURL(t, gw) + "/flaky"

	for i := 0; i < 2; i++ {
		resp, _ := http.Get(url)
		resp.Body.Close()
		if resp.StatusCode != 500 {
			t.Fatalf("request %d = %d, want upstream 500", i, resp.StatusCode)
		}
	}

	resp, _ := http.Get(url)
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 from open breaker", resp.StatusCode)
	}
	if served.Load() != 2 {
		t.Errorf("upstream served %d requests, want 2 (third blocked by breaker)", served.Load())
	}
}

func TestHeaderManipulation(t *testing.T) {
	up := newUpstream(t)
	var gotHeader atomic.Pointer[string]
	up.on("/hdr", func(w http.ResponseWriter, r *http.Request) {
		v := r.Header.Get("X-Extra")
		gotHeader.Store(&v)
		w.Header().Set("X-Upstream-Remove", "secret")
		w.Header().Set("X-Upstream-Keep", "yes")
	})

	be := &config.Backend{
		Name: "api",
		URL:  up.url(),
		Routes: []config.Route{
			routeCfg("GET", "/hdr", func(r *config.Route) {
				r.Headers = &config.HeadersConfig{
					Request: &config.HeaderRules{
						Set: map[string]string{"X-Extra": "gateway"},
					},
					Response: &config.HeaderRules{
						Set:    map[string]string{"X-Gateway": "zin"},
						Remove: []string{"X-Upstream-Remove"},
					},
				}
			}),
		},
	}
	gw := buildGateway(t, be)

	resp, _ := http.Get(serveURL(t, gw) + "/hdr")
	resp.Body.Close()

	if got := *gotHeader.Load(); got != "gateway" {
		t.Errorf("upstream X-Extra = %q, want \"gateway\"", got)
	}
	if resp.Header.Get("X-Gateway") != "zin" {
		t.Errorf("response X-Gateway = %q, want \"zin\"", resp.Header.Get("X-Gateway"))
	}
	if resp.Header.Get("X-Upstream-Remove") != "" {
		t.Error("X-Upstream-Remove should be removed from response")
	}
	if resp.Header.Get("X-Upstream-Keep") != "yes" {
		t.Error("X-Upstream-Keep should pass through")
	}
}

func TestStaticRouteNoUpstreamCall(t *testing.T) {
	up := newUpstream(t)
	be := &config.Backend{
		Name: "api",
		URL:  up.url(),
		Routes: []config.Route{
			routeCfg("GET", "/ping", func(r *config.Route) {
				r.Static = &config.StaticConfig{
					Status: 200,
					Body:   []byte(`{"ok":true}`),
				}
			}),
		},
	}
	gw := buildGateway(t, be)

	resp, err := http.Get(serveURL(t, gw) + "/ping")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q", ct)
	}
	if up.count("/ping") != 0 {
		t.Error("static route must not reach upstream")
	}
}

func TestTargetRewrite(t *testing.T) {
	up := newUpstream(t)
	be := &config.Backend{
		Name: "api",
		URL:  up.url(),
		Routes: []config.Route{
			routeCfg("GET", "/public/items/{id}", func(r *config.Route) {
				r.Target = "/api/items/{id}"
			}),
		},
	}
	gw := buildGateway(t, be)

	resp, err := http.Get(serveURL(t, gw) + "/public/items/42")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if up.count("/api/items/42") != 1 {
		t.Errorf("upstream saw %d calls to /api/items/42, want 1", up.count("/api/items/42"))
	}
}

func TestNotFound(t *testing.T) {
	up := newUpstream(t)
	gw := buildGateway(t, &config.Backend{
		Name: "api", URL: up.url(),
		Routes: []config.Route{routeCfg("GET", "/items", nil)},
	})

	resp, err := http.Get(serveURL(t, gw) + "/nope")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// serveURL runs the gateway on a random port for the test duration.
func serveURL(t *testing.T, h http.Handler) string {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL
}

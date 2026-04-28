package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

var (
	reset  = "\033[0m"
	red    = "\033[31m"
	green  = "\033[32m"
	yellow = "\033[33m"
	blue   = "\033[34m"
	cyan   = "\033[36m"
)

// ---------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------

type Config struct {
	Listen   string    `json:"listen"`
	Backends []Backend `json:"backends"`
}

type Backend struct {
	Name   string  `json:"name"`
	URL    string  `json:"url"`
	Routes []Route `json:"routes"`
}

type Route struct {
	Method  string   `json:"method"`
	Pattern string   `json:"pattern"`
	Target  string   `json:"target"`
	Params  []string `json:"-"`
}

var pathParamRE = regexp.MustCompile(`\{([a-zA-Z0-9_]+)(?:\.\.\.)?\}`)

func extractParams(pattern string) []string {
	matches := pathParamRE.FindAllStringSubmatch(pattern, -1)
	out := make([]string, len(matches))
	for i, m := range matches {
		out[i] = m[1]
	}
	return out
}

// ---------------------------------------------------------------------
// Hot-reloadable mux
// ---------------------------------------------------------------------

type atomicMux struct {
	mu  sync.RWMutex
	mux *http.ServeMux
}

func (a *atomicMux) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	mux := a.mux
	a.mu.RUnlock()
	mux.ServeHTTP(w, r)
}

func (a *atomicMux) Swap(mux *http.ServeMux) {
	a.mu.Lock()
	a.mux = mux
	a.mu.Unlock()
}

// ---------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------

func main() {
	am := &atomicMux{}

	// Initial load
	cfg, mux := buildMux()
	am.Swap(mux)

	// Start file watcher
	go watchConfig(am)

	addr := cfg.Listen
	if addr == "" {
		addr = ":8080"
	}
	log.Printf("Gateway listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, corsMiddleware(am)))
}

// ---------------------------------------------------------------------
// Build mux from config
// ---------------------------------------------------------------------

func buildMux() (Config, *http.ServeMux) {
	cfg := loadConfig()

	mux := http.NewServeMux()

	for _, be := range cfg.Backends {
		bp, err := newBackendProxy(be.URL)
		if err != nil {
			log.Printf("invalid backend URL %q: %v", be.URL, err)
			continue
		}

		for i := range be.Routes {
			route := &be.Routes[i]
			route.Params = extractParams(route.Pattern)

			h := makeHandler(route, bp)

			if route.Method != "" && route.Method != "*" {
				mux.HandleFunc(route.Method+" "+route.Pattern, h)
			} else {
				mux.HandleFunc(route.Pattern, h)
			}
		}
	}

	// Catch-all for 404 logging
	mux.HandleFunc("/{path...}", func(w http.ResponseWriter, req *http.Request) {
		http.NotFound(w, req)
		logRequest(req, http.StatusNotFound, "-")
	})

	return cfg, mux
}

// ---------------------------------------------------------------------
// File watcher
// ---------------------------------------------------------------------

func watchConfig(am *atomicMux) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatalf("failed to create watcher: %v", err)
	}
	defer watcher.Close()

	// Watch the file itself (handle renames/atomic writes)
	if err := watcher.Add("config.json"); err != nil {
		log.Fatalf("failed to watch config.json: %v", err)
	}

	// Debounce rapid events
	var debounceTimer *time.Timer
	var mu sync.Mutex

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) == 0 {
				continue
			}

			mu.Lock()
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			debounceTimer = time.AfterFunc(200*time.Millisecond, func() {
				log.Println("Config changed, reloading...")

				// Re-add watch in case of rename/replace
				watcher.Remove("config.json")
				watcher.Add("config.json")

				_, newMux := buildMux()
				am.Swap(newMux)

				log.Println("Config reloaded successfully")
			})
			mu.Unlock()

		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			log.Printf("watcher error: %v", err)
		}
	}
}

// ---------------------------------------------------------------------
// Config loading
// ---------------------------------------------------------------------

func loadConfig() Config {
	data, err := os.ReadFile("config.json")
	if err != nil {
		log.Fatalf("config.json required: %v", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		log.Fatalf("invalid config.json: %v", err)
	}
	return cfg
}

// ---------------------------------------------------------------------
// Proxy
// ---------------------------------------------------------------------

type backendProxy struct {
	proxy *httputil.ReverseProxy
}

func newBackendProxy(rawURL string) (*backendProxy, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	p := httputil.NewSingleHostReverseProxy(u)
	p.ModifyResponse = func(resp *http.Response) error {
		for _, h := range []string{
			"Access-Control-Allow-Origin",
			"Access-Control-Allow-Methods",
			"Access-Control-Allow-Headers",
			"Access-Control-Allow-Credentials",
			"Access-Control-Expose-Headers",
			"Access-Control-Max-Age",
		} {
			resp.Header.Del(h)
		}
		return nil
	}
	return &backendProxy{proxy: p}, nil
}

func makeHandler(route *Route, bp *backendProxy) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		req = req.Clone(req.Context())

		path := route.Pattern
		if route.Target != "" {
			path = route.Target
		}
		for _, p := range route.Params {
			v := req.PathValue(p)
			path = strings.ReplaceAll(path, "{"+p+"}", v)
			path = strings.ReplaceAll(path, "{"+p+"...}", v)
		}
		req.URL.Path = path
		req.URL.RawPath = ""

		rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		bp.proxy.ServeHTTP(rw, req)

		logRequest(req, rw.statusCode, path)
	}
}

// ---------------------------------------------------------------------
// CORS
// ---------------------------------------------------------------------

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		origin := req.Header.Get("Origin")
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		} else {
			w.Header().Set("Access-Control-Allow-Origin", "*")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, PATCH, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Cookie, Accept-Language")
		w.Header().Set("Access-Control-Allow-Credentials", "true")

		if req.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, req)
	})
}

// ---------------------------------------------------------------------
// Response writer + logging
// ---------------------------------------------------------------------

type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (rw *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := rw.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

func logRequest(req *http.Request, status int, backendPath string) {
	methodColor := colorForMethod(req.Method)
	statusColor := colorForStatus(status)

	fmt.Fprintf(os.Stderr, "%s[%s]%s %s%s%s %s%s%s %s%d%s -> %s%s%s\n",
		cyan, time.Now().Format("15:04:05"), reset,
		methodColor, req.Method, reset,
		yellow, req.URL.Path, reset,
		statusColor, status, reset,
		green, backendPath, reset,
	)
}

func colorForMethod(method string) string {
	switch method {
	case "GET":
		return green
	case "POST":
		return blue
	case "PUT":
		return yellow
	case "PATCH":
		return cyan
	case "DELETE":
		return red
	default:
		return reset
	}
}

func colorForStatus(status int) string {
	if status >= 200 && status < 300 {
		return green
	}
	if status >= 400 {
		return red
	}
	return yellow
}

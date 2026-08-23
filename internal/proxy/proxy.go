// Package proxy builds the reverse proxy toward a configured backend
// and the handler that rewrites request paths per route.
package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/aansl/zin/internal/config"
	"github.com/aansl/zin/internal/httpx"
	"github.com/aansl/zin/internal/logging"
	"github.com/aansl/zin/internal/qos"
)

// Backend bundles the upstream URL with its optional circuit breaker.
// Each route gets its own ReverseProxy instance built from it.
type Backend struct {
	url     *url.URL
	breaker *qos.Breaker
}

func NewBackend(be *config.Backend) (*Backend, error) {
	u, err := url.Parse(be.URL)
	if err != nil {
		return nil, err
	}
	b := &Backend{url: u}
	if be.Breaker != nil {
		b.breaker = qos.NewBreaker(be.Breaker.MaxFailures, be.Breaker.OpenTimeout.Duration)
	}
	return b, nil
}

// corsHeaders are handled at the gateway edge; strip them from upstream
// responses so they do not conflict with our own.
var corsHeaders = []string{
	"Access-Control-Allow-Origin",
	"Access-Control-Allow-Methods",
	"Access-Control-Allow-Headers",
	"Access-Control-Allow-Credentials",
	"Access-Control-Expose-Headers",
	"Access-Control-Max-Age",
}

// Handler returns a handler for one route: rewrites the request path
// (target substitution with path params), proxies it, applies response
// header rules and records breaker outcomes.
func (b *Backend) Handler(route *config.Route) http.HandlerFunc {
	// Per-route proxy so ModifyResponse can apply this route's rules.
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(b.url)
			pr.Out.Host = b.url.Host
		},
		ModifyResponse: func(resp *http.Response) error {
			for _, h := range corsHeaders {
				resp.Header.Del(h)
			}
			if route.Headers != nil && route.Headers.Response != nil {
				httpx.ApplyHeaderRules(resp.Header, route.Headers.Response)
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if errors.Is(err, context.DeadlineExceeded) {
				http.Error(w, "gateway timeout", http.StatusGatewayTimeout)
				return
			}
			http.Error(w, "bad gateway", http.StatusBadGateway)
		},
	}

	return func(w http.ResponseWriter, req *http.Request) {
		if b.breaker != nil {
			if err := b.breaker.Allow(); err != nil {
				http.Error(w, "backend unavailable (circuit open)", http.StatusServiceUnavailable)
				logging.LogRequest(req, http.StatusServiceUnavailable, "-")
				return
			}
		}

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

		rw := &httpx.StatusWriter{ResponseWriter: w}
		rp.ServeHTTP(rw, req)

		// Transport errors surface as the ErrorHandler's 502/504, so a
		// status check here covers both upstream and network failures.
		if b.breaker != nil {
			if rw.Status() >= 500 {
				b.breaker.RecordFailure()
			} else {
				b.breaker.RecordSuccess()
			}
		}
		logging.LogRequest(req, rw.Status(), path)
	}
}

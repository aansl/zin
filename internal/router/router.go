// Package router assembles the request router from configuration: each
// route gets a Krakend-style pipeline of middlewares in front of its
// proxy or static handler.
package router

import (
	"net/http"

	"github.com/aansl/zin/internal/config"
	"github.com/aansl/zin/internal/httpx"
	"github.com/aansl/zin/internal/logging"
	"github.com/aansl/zin/internal/proxy"
)

// Build constructs the ServeMux for a loaded configuration. Every route
// passes through, outermost first:
//
//	rate limit -> cache -> timeout -> request headers -> handler
//
// Middleware stages absent from the route config are skipped. Rate
// limiter, cache and breaker instances are rebuilt with the mux on
// every reload, so their state resets when the config changes.
func Build(cfg *config.Config) (*http.ServeMux, error) {
	mux := http.NewServeMux()

	for i := range cfg.Backends {
		be := &cfg.Backends[i]
		bp, err := proxy.NewBackend(be)
		if err != nil {
			return nil, err
		}

		for j := range be.Routes {
			route := &be.Routes[j]

			var h http.Handler
			if route.Static != nil {
				h = proxy.Static(route.Static)
			} else {
				h = bp.Handler(route)
			}

			var mws []httpx.Middleware
			if m := httpx.RateLimit(route.RateLimit); m != nil {
				mws = append(mws, m)
			}
			if route.Cache != nil {
				if m := httpx.Cache(route.Cache.TTL.Duration); m != nil {
					mws = append(mws, m)
				}
			}
			if route.Timeout != nil {
				mws = append(mws, httpx.Timeout(route.Timeout.Duration))
			}
			if m := httpx.RequestHeaders(routeHeadersRequest(route)); m != nil {
				mws = append(mws, m)
			}
			h = httpx.Chain(h, mws...)

			if route.Method != "" && route.Method != "*" {
				mux.HandleFunc(route.Method+" "+route.Pattern, h.ServeHTTP)
			} else {
				mux.HandleFunc(route.Pattern, h.ServeHTTP)
			}
		}
	}

	mux.HandleFunc("/{path...}", func(w http.ResponseWriter, req *http.Request) {
		http.NotFound(w, req)
		logging.LogRequest(req, http.StatusNotFound, "-")
	})

	return mux, nil
}

func routeHeadersRequest(route *config.Route) *config.HeaderRules {
	if route.Headers == nil {
		return nil
	}
	return route.Headers.Request
}

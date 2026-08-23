// Package httpx provides composable HTTP middleware primitives used to
// assemble per-route pipelines: chaining, timeouts, rate limiting,
// caching, header rules, CORS and response capture.
package httpx

import "net/http"

// Middleware wraps a handler. Chain(h, A, B) produces A(B(h)): the first
// middleware is the outermost and runs first on the way in.
type Middleware func(http.Handler) http.Handler

func Chain(h http.Handler, mws ...Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

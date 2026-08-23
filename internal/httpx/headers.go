package httpx

import (
	"net/http"

	"github.com/aansl/zin/internal/config"
)

// RequestHeaders applies header rules to the outgoing proxied request.
func RequestHeaders(rules *config.HeaderRules) Middleware {
	if rules == nil {
		return nil
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ApplyHeaderRules(r.Header, rules)
			next.ServeHTTP(w, r)
		})
	}
}

// ApplyHeaderRules mutates h in place: remove, then set (replace), then
// add (append) — set wins over remove of the same name.
func ApplyHeaderRules(h http.Header, rules *config.HeaderRules) {
	if rules == nil {
		return
	}
	for _, name := range rules.Remove {
		h.Del(name)
	}
	for name, v := range rules.Set {
		h.Set(name, v)
	}
	for name, v := range rules.Add {
		h.Add(name, v)
	}
}

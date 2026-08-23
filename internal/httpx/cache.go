package httpx

import (
	"net/http"
	"time"

	"github.com/aansl/zin/internal/qos"
)

// Cache returns middleware that serves GET responses from cache and
// stores a snapshot of successful (2xx) upstream responses. The cache
// key is derived from method + full request URI. Returns nil when TTL
// is zero (caching disabled).
func Cache(ttl time.Duration) Middleware {
	if ttl <= 0 {
		return nil
	}
	cache := qos.NewCache()
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				next.ServeHTTP(w, r)
				return
			}

			key := r.Method + "|" + r.URL.RequestURI()
			now := time.Now()
			if hit := cache.Get(key, now); hit != nil {
				for k, vs := range hit.Header {
					for _, v := range vs {
						w.Header().Add(k, v)
					}
				}
				w.Header().Set("X-Cache", "HIT")
				w.WriteHeader(hit.Status)
				w.Write(hit.Body)
				return
			}

			rec := NewRecorder(w)
			next.ServeHTTP(rec, r)
			if rec.Status() >= 200 && rec.Status() < 300 {
				status, header, body := rec.Snapshot()
				cache.Put(key, status, header, body, ttl, time.Now())
			}
		})
	}
}

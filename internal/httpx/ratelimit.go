package httpx

import (
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/aansl/zin/internal/config"
	"github.com/aansl/zin/internal/qos"
)

// RateLimit returns a middleware enforcing cfg per key. It returns nil
// when the route has no rate limit configured.
func RateLimit(cfg *config.RateLimitConfig) Middleware {
	if cfg == nil {
		return nil
	}
	limiter := qos.NewRateLimiter(cfg.Max, cfg.Per.Duration)
	key := KeyExtractor(cfg.Key)
	retryAfter := strconv.Itoa(int(math.Ceil(cfg.Per.Duration.Seconds())))

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !limiter.Allow(key(r)) {
				w.Header().Set("Retry-After", retryAfter)
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// KeyExtractor builds the per-request bucket key from the configured
// mode: "global" (default), "ip", or "header:NAME".
func KeyExtractor(mode string) func(*http.Request) string {
	if strings.HasPrefix(mode, "header:") {
		name := mode[7:]
		return func(r *http.Request) string {
			return r.Header.Get(name)
		}
	}
	if mode == "ip" {
		return ClientIP
	}
	return func(*http.Request) string { return "global" }
}

// ClientIP prefers the first hop of X-Forwarded-For (set by the edge
// proxy in front of the gateway) and falls back to the remote address.
func ClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

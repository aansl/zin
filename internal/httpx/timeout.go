package httpx

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"time"
)

// Timeout bounds the time a handler may take. On expiry the upstream
// request context is cancelled and the client receives 504 — unless the
// handler already started writing, in which case the connection is
// marked for close and the truncated response wins.
func Timeout(d time.Duration) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()

			tw := &timeoutWriter{ResponseWriter: w}
			done := make(chan struct{}, 1)

			go func() {
				next.ServeHTTP(tw, r.WithContext(ctx))
				done <- struct{}{}
			}()

			select {
			case <-done:
			case <-ctx.Done():
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
					tw.timedOut.Store(true)
					if !tw.headerWritten.Load() {
						w.Header().Set("Content-Type", "text/plain; charset=utf-8")
						w.WriteHeader(http.StatusGatewayTimeout)
						w.Write([]byte("gateway timeout"))
					} else {
						// Headers already sent; a 504 is impossible, so
						// just ask the server to drop the connection.
						w.Header().Set("Connection", "close")
					}
				}
				// context.Canceled means the client went away; nothing to send.
			}
		})
	}
}

// timeoutWriter suppresses writes from a handler that lost the race
// against the deadline, so a timed-out handler cannot corrupt the 504.
type timeoutWriter struct {
	http.ResponseWriter
	headerWritten atomic.Bool
	timedOut      atomic.Bool
}

func (w *timeoutWriter) WriteHeader(code int) {
	if w.timedOut.Load() {
		return
	}
	w.headerWritten.Store(true)
	w.ResponseWriter.WriteHeader(code)
}

func (w *timeoutWriter) Write(b []byte) (int, error) {
	if w.timedOut.Load() {
		return len(b), nil // swallow; the request is already answered
	}
	w.headerWritten.Store(true)
	return w.ResponseWriter.Write(b)
}

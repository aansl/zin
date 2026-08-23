package router

import (
	"net/http"
	"sync"
)

// AtomicMux is an http.Handler whose underlying router can be swapped
// atomically. In-flight requests finish on the old router; new requests
// immediately see the new one. This is what makes hot reload zero-downtime.
type AtomicMux struct {
	mu  sync.RWMutex
	mux *http.ServeMux
}

func (a *AtomicMux) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	mux := a.mux
	a.mu.RUnlock()
	mux.ServeHTTP(w, r)
}

func (a *AtomicMux) Swap(mux *http.ServeMux) {
	a.mu.Lock()
	a.mux = mux
	a.mu.Unlock()
}

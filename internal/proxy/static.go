package proxy

import (
	"net/http"

	"github.com/aansl/zin/internal/config"
	"github.com/aansl/zin/internal/logging"
)

// Static answers directly from configuration with no upstream call.
// A JSON body, when present, is served as application/json.
func Static(cfg *config.StaticConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		for k, v := range cfg.Headers {
			w.Header().Set(k, v)
		}
		if len(cfg.Body) > 0 {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(cfg.Status)
		w.Write(cfg.Body)
		logging.LogRequest(r, cfg.Status, "static")
	}
}

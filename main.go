// Command zin is a lightweight, zero-downtime API gateway: routes
// traffic to upstream services with hot-reloadable config, per-route
// caching, rate limiting, timeouts, circuit breaking, header
// manipulation and static responses.
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/aansl/zin/internal/config"
	"github.com/aansl/zin/internal/httpx"
	"github.com/aansl/zin/internal/router"
)

// discoverConfig resolves the config path: explicit flag, else ./config
// directory if present, else ./config.json.
func discoverConfig(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	if info, err := os.Stat("config"); err == nil && info.IsDir() {
		return "config"
	}
	return "config.json"
}

func main() {
	configPath := flag.String("config", "", "path to config file or directory (default: ./config if present, else ./config.json)")
	flag.Parse()

	path := discoverConfig(*configPath)

	cfg, root, err := config.Load(path)
	if err != nil {
		log.Fatal(err)
	}

	am := &router.AtomicMux{}
	mux, err := router.Build(cfg)
	if err != nil {
		log.Fatal(err)
	}
	am.Swap(mux)

	// A single-file config only reacts to that file; a config directory
	// reloads on any change inside it.
	only := ""
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		only = filepath.Base(path)
	}
	if _, err := config.Watch(root, only, func() {
		newCfg, _, err := config.Load(path)
		if err != nil {
			log.Printf("reload failed, keeping previous config: %v", err)
			return
		}
		newMux, err := router.Build(newCfg)
		if err != nil {
			log.Printf("reload failed, keeping previous config: %v", err)
			return
		}
		am.Swap(newMux)
		log.Println("config reloaded")
	}); err != nil {
		log.Fatalf("failed to watch config: %v", err)
	}

	addr := cfg.Listen
	if addr == "" {
		addr = ":8080"
	}
	log.Printf("Gateway listening on %s (config: %s)", addr, path)
	log.Fatal(http.ListenAndServe(addr, httpx.CORS(am)))
}

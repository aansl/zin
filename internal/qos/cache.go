package qos

import (
	"net/http"
	"sync"
	"time"
)

// CachedResponse is a snapshot of an upstream response ready to be
// replayed to a client.
type CachedResponse struct {
	Status int
	Header http.Header
	Body   []byte
}

type cacheEntry struct {
	resp    CachedResponse
	expires time.Time
}

// Cache is a minimal in-memory TTL cache for response snapshots. It is
// safe for concurrent use. Entries expire lazily on read.
type Cache struct {
	mu      sync.RWMutex
	entries map[string]*cacheEntry
}

func NewCache() *Cache {
	return &Cache{entries: make(map[string]*cacheEntry)}
}

// Get returns the cached response for key, or nil if absent or expired.
func (c *Cache) Get(key string, now time.Time) *CachedResponse {
	c.mu.RLock()
	e, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok || now.After(e.expires) {
		return nil
	}
	return &e.resp
}

// Put stores a response snapshot under key until now+ttl. The header
// map is cloned so later mutations by the transport do not leak in.
func (c *Cache) Put(key string, status int, header http.Header, body []byte, ttl time.Duration, now time.Time) {
	if ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = &cacheEntry{
		resp: CachedResponse{
			Status: status,
			Header: header.Clone(),
			Body:   body,
		},
		expires: now.Add(ttl),
	}
	if len(c.entries) > 1024 {
		c.sweepLocked(now)
	}
}

// sweepLocked drops expired entries; caller holds mu.
func (c *Cache) sweepLocked(now time.Time) {
	for k, e := range c.entries {
		if now.After(e.expires) {
			delete(c.entries, k)
		}
	}
}

package server

import (
	"sync"
	"time"
)

// apiKeyCacheTTL bounds how long another instance keeps serving a key after
// it is disabled or its rate limit changes. The instance that handles the
// disable request drops its copy immediately.
const apiKeyCacheTTL = 30 * time.Second

type apiKeyInfo struct {
	disabled bool
	perSec   int
	display  string
}

// apiKeyCache keeps recently used API key rows in memory so the proxy hot
// path does not query api_keys on every request. Unknown keys are never
// cached: a key created on another instance works right away, and random
// garbage keys cannot grow the map.
type apiKeyCache struct {
	ttl     time.Duration
	mu      sync.RWMutex
	entries map[string]apiKeyEntry
}

type apiKeyEntry struct {
	info    apiKeyInfo
	expires time.Time
}

func newAPIKeyCache(ttl time.Duration) *apiKeyCache {
	return &apiKeyCache{ttl: ttl, entries: make(map[string]apiKeyEntry)}
}

func (c *apiKeyCache) get(hash string) (apiKeyInfo, bool) {
	c.mu.RLock()
	e, ok := c.entries[hash]
	c.mu.RUnlock()
	if !ok || time.Now().After(e.expires) {
		return apiKeyInfo{}, false
	}
	return e.info, true
}

func (c *apiKeyCache) put(hash string, info apiKeyInfo) {
	c.mu.Lock()
	c.entries[hash] = apiKeyEntry{info: info, expires: time.Now().Add(c.ttl)}
	c.mu.Unlock()
}

func (c *apiKeyCache) forget(hash string) {
	c.mu.Lock()
	delete(c.entries, hash)
	c.mu.Unlock()
}

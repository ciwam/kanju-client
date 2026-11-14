package backend

import (
	"sync"
	"time"
)

// Server-wide configuration constants. Mirrors the original Spring Boot application.properties.
const (
	DefaultPort   = "28088"
	DefaultDBPath = "data/kanju.db"

	// TokenExpiry is how long a login token stays valid (168h = 7 days).
	TokenExpiry = 168 * time.Hour

	// Douban / scraping timeouts.
	fetchReadTimeout = 15 * time.Second

	// Source-availability probing (filters dead "searchable but unplayable"
	// sources before they are shown to the user). See backend/availability.go.
	//
	// probeMediaTimeout bounds a single media reachability probe.
	// availabilityCheckTimeout bounds the whole check for one source
	// (detail fetch + up to maxProbeTotal play-url resolutions + probes).
	probeMediaTimeout        = 8 * time.Second
	availabilityCheckTimeout = 30 * time.Second

	// How many channels (线路) and episodes per channel we sample when proving
	// a source is alive, and the overall cap on probes per source. We only
	// need ONE working episode to keep the source, so keep this small to bound
	// latency; covering the first few channels still satisfies "检查各选集".
	maxChannelsToCheck           = 3
	maxEpisodesPerChannelToCheck = 1
	maxProbeTotal                = 3
	availabilityMediaProbeBytes  = 64 * 1024

	// Result cache TTLs. A proven-good source is trusted for a while so
	// repeated searches (different films) don't re-probe; a failed probe is
	// cached shorter so a transient outage recovers sooner.
	availabilitySuccessTTL = 15 * time.Minute
	availabilityFailureTTL = 5 * time.Minute
)

// tokenEntry is a cached session token.
type tokenEntry struct {
	userID    int64
	role      string
	expiresAt time.Time
}

// TokenCache is an in-memory, concurrency-safe cache of valid session tokens.
type TokenCache struct {
	mu sync.RWMutex
	m  map[string]tokenEntry
}

func newTokenCache() *TokenCache {
	tc := &TokenCache{m: make(map[string]tokenEntry)}
	go tc.cleanupLoop()
	return tc
}

// cleanupLoop 定期清理过期的 token 条目，防止内存无界增长。
func (c *TokenCache) cleanupLoop() {
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for range t.C {
		c.cleanup()
	}
}

// cleanup 移除所有已过期的 token 条目。
func (c *TokenCache) cleanup() {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for k, e := range c.m {
		if now.After(e.expiresAt) {
			delete(c.m, k)
		}
	}
}

func (c *TokenCache) get(token string) (tokenEntry, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.m[token]
	if !ok {
		return tokenEntry{}, false
	}
	if time.Now().After(e.expiresAt) {
		return tokenEntry{}, false
	}
	return e, true
}

func (c *TokenCache) put(token string, e tokenEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[token] = e
}

func (c *TokenCache) delete(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, token)
}

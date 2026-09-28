package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/Gentleman-Programming/engram/v2/internal/cloud/remote"
)

const (
	// bearerCachePositiveTTL/bearerCacheNegativeTTL are deliberately
	// asymmetric: a token that was valid a moment ago is still very likely
	// valid, but a token that was just rejected should be re-checked sooner
	// (an operator fixing a typo'd token shouldn't wait minutes to retry).
	bearerCachePositiveTTL = 5 * time.Minute
	bearerCacheNegativeTTL = 30 * time.Second

	// bearerCacheMaxEntries bounds the cache so a stream of distinct garbage
	// tokens cannot grow it unbounded (T2 requirement). A single-user HTTP
	// deployment realistically sees one or two distinct tokens at a time.
	bearerCacheMaxEntries = 256
)

type bearerCacheEntry struct {
	valid     bool
	expiresAt time.Time
}

// bearerValidationCache caches Engram Cloud bearer-token validation results
// keyed by sha256(token) — the raw token is never retained — with separate
// positive/negative TTLs and a bounded entry count.
type bearerValidationCache struct {
	mu      sync.Mutex
	entries map[string]bearerCacheEntry
	now     func() time.Time
}

func newBearerValidationCache() *bearerValidationCache {
	return &bearerValidationCache{
		entries: make(map[string]bearerCacheEntry),
		now:     time.Now,
	}
}

func bearerCacheKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CheckOrValidate returns the cached validity for token if the cached entry
// is still fresh; otherwise it calls validate, caches the outcome, and
// returns it. validate's error distinguishes a definite rejection
// (remote.ErrBearerInvalid, cached with the shorter negative TTL) from an
// unreachable cloud server (returned as-is and never cached, so a transient
// outage cannot poison the cache with a false negative).
func (c *bearerValidationCache) CheckOrValidate(token string, validate func() error) (bool, error) {
	key := bearerCacheKey(token)
	now := c.now()

	c.mu.Lock()
	if entry, ok := c.entries[key]; ok && now.Before(entry.expiresAt) {
		c.mu.Unlock()
		return entry.valid, nil
	}
	c.mu.Unlock()

	err := validate()
	if err == nil {
		c.store(key, true, bearerCachePositiveTTL)
		return true, nil
	}
	if errors.Is(err, remote.ErrBearerInvalid) {
		c.store(key, false, bearerCacheNegativeTTL)
		return false, nil
	}
	return false, err
}

func (c *bearerValidationCache) store(key string, valid bool, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[key]; !exists && len(c.entries) >= bearerCacheMaxEntries {
		c.evictExpiredLocked()
		if len(c.entries) >= bearerCacheMaxEntries {
			// Still over the bound after sweeping expired entries: evict one
			// arbitrary entry. Go's map iteration order is randomized, which
			// is an acceptable simple bound for a single-user deployment.
			for k := range c.entries {
				delete(c.entries, k)
				break
			}
		}
	}
	c.entries[key] = bearerCacheEntry{valid: valid, expiresAt: c.now().Add(ttl)}
}

func (c *bearerValidationCache) evictExpiredLocked() {
	now := c.now()
	for k, e := range c.entries {
		if !now.Before(e.expiresAt) {
			delete(c.entries, k)
		}
	}
}

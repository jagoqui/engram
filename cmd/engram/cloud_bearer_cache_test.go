package main

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v2/internal/cloud/remote"
)

func TestBearerValidationCache_HitAvoidsSecondValidateCall(t *testing.T) {
	c := newBearerValidationCache()
	calls := 0
	validate := func() error { calls++; return nil }

	for i := 0; i < 3; i++ {
		valid, err := c.CheckOrValidate("token-a", validate)
		if err != nil || !valid {
			t.Fatalf("CheckOrValidate = (%v, %v); want (true, nil)", valid, err)
		}
	}
	if calls != 1 {
		t.Fatalf("validate called %d times; want 1 (cache hit should avoid re-validating)", calls)
	}
}

func TestBearerValidationCache_PositiveTTLExpires(t *testing.T) {
	c := newBearerValidationCache()
	now := time.Now()
	c.now = func() time.Time { return now }
	calls := 0
	validate := func() error { calls++; return nil }

	if _, err := c.CheckOrValidate("token-a", validate); err != nil {
		t.Fatalf("first CheckOrValidate: %v", err)
	}
	now = now.Add(bearerCachePositiveTTL + time.Second)
	if _, err := c.CheckOrValidate("token-a", validate); err != nil {
		t.Fatalf("second CheckOrValidate: %v", err)
	}
	if calls != 2 {
		t.Fatalf("validate called %d times after positive TTL expiry; want 2", calls)
	}
}

func TestBearerValidationCache_NegativeTTLExpiresIndependently(t *testing.T) {
	c := newBearerValidationCache()
	now := time.Now()
	c.now = func() time.Time { return now }

	calls := 0
	validate := func() error { calls++; return remote.ErrBearerInvalid }

	valid, err := c.CheckOrValidate("bad-token", validate)
	if err != nil || valid {
		t.Fatalf("CheckOrValidate = (%v,%v); want (false,nil)", valid, err)
	}
	// Still within the (shorter) negative TTL: cached, no re-validation.
	if _, err := c.CheckOrValidate("bad-token", validate); err != nil {
		t.Fatalf("cached negative check: %v", err)
	}
	if calls != 1 {
		t.Fatalf("validate called %d times within negative TTL; want 1", calls)
	}

	now = now.Add(bearerCacheNegativeTTL + time.Millisecond)
	if _, err := c.CheckOrValidate("bad-token", validate); err != nil {
		t.Fatalf("post-expiry check: %v", err)
	}
	if calls != 2 {
		t.Fatalf("validate called %d times after negative TTL expiry; want 2", calls)
	}
}

func TestBearerValidationCache_UnreachableIsNeverCached(t *testing.T) {
	c := newBearerValidationCache()
	calls := 0
	validate := func() error { calls++; return errors.New("network down") }

	for i := 0; i < 2; i++ {
		valid, err := c.CheckOrValidate("token", validate)
		if valid || err == nil {
			t.Fatalf("CheckOrValidate = (%v,%v); want (false, non-nil)", valid, err)
		}
	}
	if calls != 2 {
		t.Fatalf("validate called %d times; want 2 (an unreachable result must never be cached)", calls)
	}
}

func TestBearerValidationCache_BoundedSize(t *testing.T) {
	c := newBearerValidationCache()
	validate := func() error { return nil }

	for i := 0; i < bearerCacheMaxEntries*2; i++ {
		token := fmt.Sprintf("token-%d", i)
		if _, err := c.CheckOrValidate(token, validate); err != nil {
			t.Fatalf("CheckOrValidate(%d): %v", i, err)
		}
	}

	c.mu.Lock()
	n := len(c.entries)
	c.mu.Unlock()
	if n > bearerCacheMaxEntries {
		t.Fatalf("cache grew to %d entries; want <= %d (bounded size)", n, bearerCacheMaxEntries)
	}
}

package main

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v2/internal/cloud/remote"
)

func TestCloudBearerAuthenticator_ValidBearerPassesAndOverridesSyncToken(t *testing.T) {
	var gotSetToken string
	setCalls := 0
	a := newCloudBearerAuthenticator("https://cloud.example.test", "fallback-token",
		func(token string) { setCalls++; gotSetToken = token },
		func(project string) error { return nil },
	)
	a.validate = func(_, _ string) (string, error) { return "acct-1", nil }

	ok, err := a.Authenticate(context.Background(), "bearer-token", "demo")
	if err != nil || !ok {
		t.Fatalf("Authenticate = (%v, %v); want (true, nil)", ok, err)
	}
	if setCalls != 1 || gotSetToken != "bearer-token" {
		t.Fatalf("setSyncToken called %d times with %q; want 1 call with the validated bearer", setCalls, gotSetToken)
	}
}

func TestCloudBearerAuthenticator_InvalidBearerRejected(t *testing.T) {
	a := newCloudBearerAuthenticator("https://cloud.example.test", "", nil, nil)
	a.validate = func(_, _ string) (string, error) { return "", remote.ErrBearerInvalid }

	ok, err := a.Authenticate(context.Background(), "bad-token", "demo")
	if err != nil || ok {
		t.Fatalf("Authenticate = (%v, %v); want (false, nil) for a rejected bearer", ok, err)
	}
}

func TestCloudBearerAuthenticator_UnreachableCloudReturnsError(t *testing.T) {
	a := newCloudBearerAuthenticator("https://cloud.example.test", "", nil, nil)
	wantErr := errors.New("dial tcp: connection refused")
	a.validate = func(_, _ string) (string, error) { return "", wantErr }

	ok, err := a.Authenticate(context.Background(), "some-token", "demo")
	if ok || !errors.Is(err, wantErr) {
		t.Fatalf("Authenticate = (%v, %v); want (false, wantErr) when cloud is unreachable", ok, err)
	}
}

func TestCloudBearerAuthenticator_NoBearerFallsBackToEnvToken(t *testing.T) {
	validateCalls := 0
	a := newCloudBearerAuthenticator("https://cloud.example.test", "env-token", nil, nil)
	a.validate = func(_, _ string) (string, error) { validateCalls++; return "acct-1", nil }

	ok, err := a.Authenticate(context.Background(), "", "demo")
	if err != nil || !ok {
		t.Fatalf("Authenticate = (%v, %v); want (true, nil) via env fallback", ok, err)
	}
	if validateCalls != 1 { // now validated once, to learn its owner principal
		t.Fatalf("validate called %d times; want 1 (once, to bind the owner principal)", validateCalls)
	}
}

func TestCloudBearerAuthenticator_NoBearerNoFallbackRejected(t *testing.T) {
	a := newCloudBearerAuthenticator("https://cloud.example.test", "", nil, nil)
	ok, err := a.Authenticate(context.Background(), "", "demo")
	if err != nil || ok {
		t.Fatalf("Authenticate = (%v, %v); want (false, nil) with no bearer and no fallback token", ok, err)
	}
}

func TestCloudBearerAuthenticator_EnrollsRequestProjectOnce(t *testing.T) {
	enrollCalls := 0
	var gotProject string
	a := newCloudBearerAuthenticator("https://cloud.example.test", "env-token", nil,
		func(project string) error { enrollCalls++; gotProject = project; return nil },
	)
	a.validate = func(_, _ string) (string, error) { return "acct-1", nil }

	for i := 0; i < 3; i++ {
		if ok, err := a.Authenticate(context.Background(), "", "demo"); err != nil || !ok {
			t.Fatalf("Authenticate = (%v, %v); want (true, nil)", ok, err)
		}
	}
	if enrollCalls != 1 || gotProject != "demo" {
		t.Fatalf("enrollProject called %d times with %q; want exactly 1 call with %q", enrollCalls, gotProject, "demo")
	}
}

func TestCloudBearerAuthenticator_TokenNeverLogged(t *testing.T) {
	var buf bytes.Buffer
	oldOut := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(oldOut) })

	const secretToken = "super-secret-token-xyz"

	invalid := newCloudBearerAuthenticator("https://cloud.example.test", "", nil, nil)
	invalid.validate = func(_, _ string) (string, error) { return "", remote.ErrBearerInvalid }
	if _, err := invalid.Authenticate(context.Background(), secretToken, "demo"); err != nil {
		t.Fatalf("Authenticate (invalid): %v", err)
	}

	unreachable := newCloudBearerAuthenticator("https://cloud.example.test", "", nil, nil)
	unreachable.validate = func(_, _ string) (string, error) { return "", errors.New("dial tcp: connection refused") }
	if _, err := unreachable.Authenticate(context.Background(), secretToken, "demo"); err == nil {
		t.Fatal("expected an unreachable error")
	}

	if strings.Contains(buf.String(), secretToken) {
		t.Fatalf("log output contains the raw bearer token: %q", buf.String())
	}
}

func TestCloudBearerAuthenticator_DifferentPrincipalRejected(t *testing.T) {
	cases := []struct {
		name       string
		fallback   string
		principals map[string]string
		requests   []string
		wantOK     []bool
	}{
		{"fallback pins owner", "fallback-token", map[string]string{"fallback-token": "acct-owner", "other-token": "acct-attacker"}, []string{"other-token"}, []bool{false}},
		{"no fallback: first bearer pins", "", map[string]string{"first-token": "acct-1", "second-token": "acct-2"}, []string{"first-token", "second-token"}, []bool{true, false}},
	}
	for _, tc := range cases {
		a := newCloudBearerAuthenticator("https://cloud.example.test", tc.fallback, nil, nil)
		a.validate = func(_, token string) (string, error) { return tc.principals[token], nil }
		for i, token := range tc.requests {
			if ok, err := a.Authenticate(context.Background(), token, "demo"); err != nil || ok != tc.wantOK[i] {
				t.Fatalf("%s: Authenticate(%q) = (%v, %v); want (%v, nil)", tc.name, token, ok, err, tc.wantOK[i])
			}
		}
	}
}

func TestCloudBearerAuthenticator_RejectedFallbackFailsClosed(t *testing.T) {
	a := newCloudBearerAuthenticator("https://cloud.example.test", "revoked-env-token", nil, nil)
	a.validate = func(_, token string) (string, error) {
		if token == "revoked-env-token" {
			return "", remote.ErrBearerInvalid
		}
		return "acct-attacker", nil
	}
	if ok, err := a.Authenticate(context.Background(), "attacker-token", "demo"); err != nil || ok {
		t.Fatalf("Authenticate = (%v, %v); want (false, nil): a rejected .env token must not let another account become owner", ok, err)
	}
}

func TestCloudBearerAuthenticator_CacheHitWithoutPrincipalRevalidates(t *testing.T) {
	a := newCloudBearerAuthenticator("https://cloud.example.test", "", nil, nil)
	a.validate = func(_, _ string) (string, error) { return "acct-1", nil }
	// Simulate the race: the cache already holds a positive entry, but the
	// validating request has not recorded its principal yet.
	a.cache.store(bearerCacheKey("bearer-token"), true, bearerCachePositiveTTL)
	if ok, err := a.Authenticate(context.Background(), "bearer-token", "demo"); err != nil || !ok {
		t.Fatalf("Authenticate = (%v, %v); want (true, nil)", ok, err)
	}
}

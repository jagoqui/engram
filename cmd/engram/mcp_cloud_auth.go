package main

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"

	"github.com/Gentleman-Programming/engram/v2/internal/cloud/remote"
	"github.com/Gentleman-Programming/engram/v2/internal/mcp"
)

// cloudBearerAuthenticator implements mcp.CloudBearerAuthenticator for
// `engram mcp --transport=http` in cloud mode (T2). It:
//   - validates the request's bearer against Engram Cloud, cached via
//     bearerValidationCache;
//   - lets a valid bearer override the token autosync uses for this
//     single-user deployment (setSyncToken — "last validated token wins");
//   - falls back to the .env-configured token when the request sent none;
//   - ensures the request's project is enrolled for sync using the existing
//     local enrollment mechanism (store.EnrollProject — idempotent), once
//     per project for the lifetime of this authenticator.
type cloudBearerAuthenticator struct {
	serverURL     string
	fallbackToken string
	validate      func(baseURL, token string) (string, error) // = remote.ValidateBearer; injectable
	cache         *bearerValidationCache
	setSyncToken  func(token string)
	enrollProject func(project string) error

	mu       sync.Mutex
	enrolled map[string]struct{} // projects already ensured enrolled this process
	// principals caches sha256(token)->principal ID; "" key = pinned owner.
	principals map[string]string
}

// newCloudBearerAuthenticator builds a cloudBearerAuthenticator. setSyncToken
// and enrollProject may be nil (e.g. when autosync itself did not start),
// in which case Authenticate still validates/authenticates but performs no
// override or enrollment side effect.
func newCloudBearerAuthenticator(serverURL, fallbackToken string, setSyncToken func(string), enrollProject func(string) error) *cloudBearerAuthenticator {
	return &cloudBearerAuthenticator{
		serverURL:     strings.TrimSpace(serverURL),
		fallbackToken: strings.TrimSpace(fallbackToken),
		validate:      remote.ValidateBearer,
		cache:         newBearerValidationCache(),
		setSyncToken:  setSyncToken,
		enrollProject: enrollProject,
		enrolled:      make(map[string]struct{}),
		principals:    make(map[string]string),
	}
}

var _ mcp.CloudBearerAuthenticator = (*cloudBearerAuthenticator)(nil)

// Authenticate implements mcp.CloudBearerAuthenticator.
func (a *cloudBearerAuthenticator) Authenticate(_ context.Context, token, project string) (bool, error) {
	effective := strings.TrimSpace(token)
	if effective == "" {
		if a.fallbackToken == "" {
			return false, nil
		}
		effective = a.fallbackToken
	}
	principalID, err := a.resolvePrincipal(effective)
	authorized := false
	if err == nil && principalID != "" {
		authorized, err = a.authorizeOwner(principalID)
	}
	if err != nil {
		log.Printf("[mcp-http] WARNING: cloud bearer validation could not reach %s: %v", a.serverURL, err)
		return false, err
	}
	if !authorized {
		log.Printf("[mcp-http] WARNING: cloud rejected the bearer, or it belongs to a different account")
		return false, nil
	}
	if a.setSyncToken != nil {
		a.setSyncToken(effective)
	}
	a.ensureEnrolled(project)
	return true, nil
}

// resolvePrincipal returns token's cloud principal ID via the cache, or "".
func (a *cloudBearerAuthenticator) resolvePrincipal(token string) (string, error) {
	key := bearerCacheKey(token)
	// record stores the principal before CheckOrValidate caches the positive
	// result, so a concurrent cache hit never sees a valid token without it.
	record := func() error {
		id, err := a.validate(a.serverURL, token)
		if err == nil {
			a.mu.Lock()
			a.principals[key] = id
			a.mu.Unlock()
		}
		return err
	}
	valid, err := a.cache.CheckOrValidate(token, record)
	if err != nil || !valid {
		return "", err
	}
	a.mu.Lock()
	id, ok := a.principals[key]
	a.mu.Unlock()
	if ok {
		return id, nil
	}
	if err := record(); err != nil { // cached as valid but principal unknown: resolve again
		if errors.Is(err, remote.ErrBearerInvalid) {
			return "", nil
		}
		return "", err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.principals[key], nil
}

// authorizeOwner reports whether principalID is the instance owner: the
// .env fallback token's principal when one is configured (a rejected
// fallback fails closed), else the first authorized bearer, pinned for the
// process lifetime.
func (a *cloudBearerAuthenticator) authorizeOwner(principalID string) (bool, error) {
	if a.fallbackToken != "" {
		owner, err := a.resolvePrincipal(a.fallbackToken)
		if err != nil {
			return false, err
		}
		if owner == "" {
			log.Printf("[mcp-http] WARNING: Engram Cloud rejects the configured cloud token; refusing every bearer until it is fixed")
		}
		return owner != "" && owner == principalID, nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	pinned, ok := a.principals[""]
	if !ok {
		a.principals[""], pinned = principalID, principalID
	}
	return pinned == principalID, nil
}

// ensureEnrolled enrolls project for cloud sync at most once per process
// lifetime, using the existing local enrollment mechanism
// (store.EnrollProject is itself idempotent — this cache only avoids a
// redundant DB write on every request).
func (a *cloudBearerAuthenticator) ensureEnrolled(project string) {
	project = strings.TrimSpace(project)
	if project == "" || a.enrollProject == nil {
		return
	}

	a.mu.Lock()
	_, done := a.enrolled[project]
	a.mu.Unlock()
	if done {
		return
	}

	if err := a.enrollProject(project); err != nil {
		log.Printf("[mcp-http] WARNING: cloud sync enrollment for project %q failed: %v", project, err)
		return
	}

	a.mu.Lock()
	a.enrolled[project] = struct{}{}
	a.mu.Unlock()
}

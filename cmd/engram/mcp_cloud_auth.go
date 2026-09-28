package main

import (
	"context"
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
	validate      func(baseURL, token string) error // = remote.ValidateBearer; injectable for tests
	cache         *bearerValidationCache
	setSyncToken  func(token string)
	enrollProject func(project string) error

	mu       sync.Mutex
	enrolled map[string]struct{} // projects already ensured enrolled this process
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
		// The .env-configured token was already resolved at startup (it is
		// what autosync itself uses absent a request bearer); trust it here
		// without a redundant cloud round-trip on every request.
		effective = a.fallbackToken
	} else {
		valid, err := a.cache.CheckOrValidate(effective, func() error { return a.validate(a.serverURL, effective) })
		if err != nil {
			log.Printf("[mcp-http] WARNING: cloud bearer validation could not reach %s: %v", a.serverURL, err)
			return false, err
		}
		if !valid {
			log.Printf("[mcp-http] WARNING: cloud rejected the request's bearer token")
			return false, nil
		}
	}

	if a.setSyncToken != nil {
		a.setSyncToken(effective)
	}
	a.ensureEnrolled(project)
	return true, nil
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

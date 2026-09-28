package remote

import (
	"errors"
	"net/http"
)

// ErrBearerInvalid is returned by ValidateBearer when the cloud server
// itself rejected the token's identity or authorization (HTTP 401 or 403).
// Any other failure — a network error, a malformed URL, or an unexpected
// status — means the cloud server could not be reached to answer the
// question at all, and ValidateBearer returns it unwrapped so callers can
// tell "definitely invalid" (401/403 — reject) apart from "could not check"
// (network/5xx — the caller should treat this as a service outage, not a
// rejection).
var ErrBearerInvalid = errors.New("cloud: bearer token rejected")

// validateBearerRoundTripper is the http.RoundTripper ValidateBearer's
// throwaway transport uses. Production always uses http.DefaultTransport
// (real TLS verification); tests may swap it to trust an
// httptest.NewTLSServer certificate, mirroring internal/tui's
// pingCloudTransport test seam.
var validateBearerRoundTripper http.RoundTripper = http.DefaultTransport

// ValidateBearer performs a cheap, read-only authenticated call against the
// cloud server to check whether token is currently accepted. It reuses the
// mutation-pull endpoint the autosync client already calls (limit=1,
// since_seq=0) instead of adding a new cloud route.
func ValidateBearer(baseURL, token string) error {
	mt, err := NewMutationTransport(baseURL, token)
	if err != nil {
		return err
	}
	mt.httpClient.Transport = validateBearerRoundTripper

	if _, err := mt.PullMutations(0, 1); err != nil {
		var statusErr *HTTPStatusError
		if errors.As(err, &statusErr) && (statusErr.StatusCode == http.StatusUnauthorized || statusErr.StatusCode == http.StatusForbidden) {
			return ErrBearerInvalid
		}
		return err
	}
	return nil
}

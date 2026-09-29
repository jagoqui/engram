package remote

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
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

// ErrIdentityBindingUnavailable is the shared class of "the cloud server is
// reachable but cannot tell us which account a bearer belongs to", a
// configuration problem (not an outage). ErrWhoAmIUnsupported and
// ErrWhoAmIAuthDisabled both match it via errors.Is.
var ErrIdentityBindingUnavailable = errors.New("cloud cannot bind a bearer token to an account")

// ErrWhoAmIUnsupported means the cloud server predates /auth/whoami (404).
var ErrWhoAmIUnsupported = fmt.Errorf("%w: cloud server does not support /auth/whoami; upgrade Engram Cloud", ErrIdentityBindingUnavailable)

// ErrWhoAmIAuthDisabled means the cloud server answered /auth/whoami with 501:
// its authentication is disabled (ENGRAM_CLOUD_INSECURE_NO_AUTH), so there is
// no principal to bind a bearer to.
var ErrWhoAmIAuthDisabled = fmt.Errorf("%w: cloud server has authentication disabled (ENGRAM_CLOUD_INSECURE_NO_AUTH); enable cloud auth", ErrIdentityBindingUnavailable)

// validateBearerRoundTripper is the http.RoundTripper ValidateBearer's
// throwaway transport uses. Production always uses http.DefaultTransport
// (real TLS verification); tests may swap it to trust an
// httptest.NewTLSServer certificate, mirroring internal/tui's
// pingCloudTransport test seam.
var validateBearerRoundTripper http.RoundTripper = http.DefaultTransport

// ValidateBearer calls /auth/whoami and returns which principal token identifies.
func ValidateBearer(baseURL, token string) (string, error) {
	mt, err := NewMutationTransport(baseURL, token)
	if err != nil {
		return "", err
	}
	mt.httpClient.Transport = validateBearerRoundTripper
	req, _ := http.NewRequest(http.MethodGet, mt.baseURL+"/auth/whoami", nil) // constant method + already-validated URL: cannot fail
	mt.setAuthorization(req)
	resp, err := mt.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		body, _ := io.ReadAll(resp.Body)
		if id := strings.TrimSpace(string(body)); id != "" {
			return id, nil
		}
		return "", errors.New("cloud: whoami response missing principal id")
	case http.StatusUnauthorized, http.StatusForbidden:
		return "", ErrBearerInvalid
	case http.StatusNotFound:
		return "", ErrWhoAmIUnsupported
	case http.StatusNotImplemented:
		return "", ErrWhoAmIAuthDisabled
	default:
		return "", newHTTPStatusError("whoami", resp.StatusCode, nil)
	}
}

package remote

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// swapValidateBearerTransport points validateBearerRoundTripper at srv's
// trusted client transport for the duration of the test, mirroring the
// mustNewMutationTransport + server.Client().Transport pattern used
// elsewhere in this package for httptest.NewTLSServer.
func swapValidateBearerTransport(t *testing.T, rt http.RoundTripper) {
	t.Helper()
	old := validateBearerRoundTripper
	validateBearerRoundTripper = rt
	t.Cleanup(func() { validateBearerRoundTripper = old })
}

func TestValidateBearer_ValidTokenReturnsPrincipalID(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/whoami" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer good-token" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte("acct-1"))
	}))
	defer srv.Close()
	swapValidateBearerTransport(t, srv.Client().Transport)

	if id, err := ValidateBearer(srv.URL, "good-token"); err != nil || id != "acct-1" {
		t.Fatalf("ValidateBearer = (%q, %v); want (acct-1, nil)", id, err)
	}
}

func TestValidateBearer_NotFoundReturnsErrWhoAmIUnsupported(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()
	swapValidateBearerTransport(t, srv.Client().Transport)

	if _, err := ValidateBearer(srv.URL, "some-token"); !errors.Is(err, ErrWhoAmIUnsupported) {
		t.Fatalf("ValidateBearer error = %v; want ErrWhoAmIUnsupported", err)
	}
}

func TestValidateBearer_UnauthorizedReturnsErrBearerInvalid(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()
	swapValidateBearerTransport(t, srv.Client().Transport)

	_, err := ValidateBearer(srv.URL, "bad-token")
	if !errors.Is(err, ErrBearerInvalid) {
		t.Fatalf("ValidateBearer error = %v; want ErrBearerInvalid", err)
	}
}

func TestValidateBearer_ForbiddenReturnsErrBearerInvalid(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
	}))
	defer srv.Close()
	swapValidateBearerTransport(t, srv.Client().Transport)

	_, err := ValidateBearer(srv.URL, "some-token")
	if !errors.Is(err, ErrBearerInvalid) {
		t.Fatalf("ValidateBearer error = %v; want ErrBearerInvalid", err)
	}
}

func TestValidateBearer_ServerErrorIsNotErrBearerInvalid(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	swapValidateBearerTransport(t, srv.Client().Transport)

	_, err := ValidateBearer(srv.URL, "some-token")
	if err == nil || errors.Is(err, ErrBearerInvalid) {
		t.Fatalf("ValidateBearer error = %v; want a non-nil error distinct from ErrBearerInvalid", err)
	}
}

func TestValidateBearer_UnreachableServerIsNotErrBearerInvalid(t *testing.T) {
	swapValidateBearerTransport(t, http.DefaultTransport)

	// Port 0 dials nothing on this host — a fast, reliable "unreachable".
	_, err := ValidateBearer("https://127.0.0.1:0", "some-token")
	if err == nil || errors.Is(err, ErrBearerInvalid) {
		t.Fatalf("ValidateBearer error = %v; want a non-nil error distinct from ErrBearerInvalid", err)
	}
}

func TestValidateBearer_NotImplementedReturnsErrWhoAmIAuthDisabled(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"auth disabled"}`, http.StatusNotImplemented)
	}))
	defer srv.Close()
	swapValidateBearerTransport(t, srv.Client().Transport)

	_, err := ValidateBearer(srv.URL, "some-token")
	if !errors.Is(err, ErrWhoAmIAuthDisabled) || !errors.Is(err, ErrIdentityBindingUnavailable) {
		t.Fatalf("ValidateBearer error = %v; want ErrWhoAmIAuthDisabled (an ErrIdentityBindingUnavailable)", err)
	}
	if !errors.Is(ErrWhoAmIUnsupported, ErrIdentityBindingUnavailable) {
		t.Fatalf("ErrWhoAmIUnsupported must also be an ErrIdentityBindingUnavailable")
	}
}

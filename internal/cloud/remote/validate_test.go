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

func TestValidateBearer_ValidTokenReturnsNil(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sync/mutations/pull" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer good-token" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"mutations":[],"has_more":false,"latest_seq":0}`))
	}))
	defer srv.Close()
	swapValidateBearerTransport(t, srv.Client().Transport)

	if err := ValidateBearer(srv.URL, "good-token"); err != nil {
		t.Fatalf("ValidateBearer: %v", err)
	}
}

func TestValidateBearer_UnauthorizedReturnsErrBearerInvalid(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()
	swapValidateBearerTransport(t, srv.Client().Transport)

	err := ValidateBearer(srv.URL, "bad-token")
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

	err := ValidateBearer(srv.URL, "some-token")
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

	err := ValidateBearer(srv.URL, "some-token")
	if err == nil || errors.Is(err, ErrBearerInvalid) {
		t.Fatalf("ValidateBearer error = %v; want a non-nil error distinct from ErrBearerInvalid", err)
	}
}

func TestValidateBearer_UnreachableServerIsNotErrBearerInvalid(t *testing.T) {
	swapValidateBearerTransport(t, http.DefaultTransport)

	// Port 0 dials nothing on this host — a fast, reliable "unreachable".
	err := ValidateBearer("https://127.0.0.1:0", "some-token")
	if err == nil || errors.Is(err, ErrBearerInvalid) {
		t.Fatalf("ValidateBearer error = %v; want a non-nil error distinct from ErrBearerInvalid", err)
	}
}

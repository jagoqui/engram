package remote

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// TestMutationTransportSetTokenOverridesAuthorizationHeader covers T2's
// "bearer override" requirement: once a per-request bearer has been
// validated against Engram Cloud, it must become the token the shared
// MutationTransport uses for subsequent push/pull calls (last validated
// token wins).
func TestMutationTransportSetTokenOverridesAuthorizationHeader(t *testing.T) {
	var gotAuth string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"mutations":[],"has_more":false,"latest_seq":0}`))
	}))
	defer server.Close()

	mt := mustNewMutationTransport(t, server.URL, "old-token")
	mt.httpClient.Transport = server.Client().Transport

	mt.SetToken("new-token")
	if _, err := mt.PullMutations(0, 1); err != nil {
		t.Fatalf("PullMutations: %v", err)
	}
	if gotAuth != "Bearer new-token" {
		t.Fatalf("Authorization = %q; want %q", gotAuth, "Bearer new-token")
	}
}

// TestMutationTransportSetTokenIsConcurrencySafe guards against a data race
// between the HTTP guard calling SetToken per-request and autosync's
// background Run loop reading the token to authorize its own push/pull
// calls. Run with -race in CI.
func TestMutationTransportSetTokenIsConcurrencySafe(t *testing.T) {
	mt := mustNewMutationTransport(t, "https://cloud.example.test", "token")

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			mt.SetToken("a")
		}
	}()
	go func() {
		defer wg.Done()
		req, _ := http.NewRequest(http.MethodGet, "https://cloud.example.test", nil)
		for i := 0; i < 200; i++ {
			mt.setAuthorization(req)
		}
	}()
	wg.Wait()
}

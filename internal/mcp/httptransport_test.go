package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	mcppkg "github.com/mark3labs/mcp-go/mcp"
)

// ─── --listen / ENGRAM_MCP_HTTP_ADDR precedence ───────────────────────────

func TestResolveHTTPListenAddr_Default(t *testing.T) {
	t.Setenv(EnvHTTPListenAddr, "")
	if got := ResolveHTTPListenAddr(""); got != DefaultHTTPListenAddr {
		t.Fatalf("ResolveHTTPListenAddr(\"\") = %q; want %q", got, DefaultHTTPListenAddr)
	}
}

func TestResolveHTTPListenAddr_EnvOverridesDefault(t *testing.T) {
	t.Setenv(EnvHTTPListenAddr, "0.0.0.0:9999")
	if got := ResolveHTTPListenAddr(""); got != "0.0.0.0:9999" {
		t.Fatalf("ResolveHTTPListenAddr(\"\") = %q; want env value", got)
	}
}

func TestResolveHTTPListenAddr_FlagWinsOverEnv(t *testing.T) {
	t.Setenv(EnvHTTPListenAddr, "0.0.0.0:9999")
	if got := ResolveHTTPListenAddr("127.0.0.1:1234"); got != "127.0.0.1:1234" {
		t.Fatalf("ResolveHTTPListenAddr(flag) = %q; want flag value", got)
	}
}

// ─── Header extraction: X-Engram-Subproject wins over its alias ──────────

func TestRequestProjectHeader_SubprojectWinsOverAlias(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("X-Engram-Subproject", "Subproject Name")
	req.Header.Set("X-Engram-Project", "alias-name")
	if got := requestProjectHeader(req); got != "subproject name" {
		t.Fatalf("requestProjectHeader = %q; want %q", got, "subproject name")
	}
}

func TestRequestProjectHeader_AliasUsedWhenSubprojectAbsent(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("X-Engram-Project", "  Alias Project  ")
	if got := requestProjectHeader(req); got != "alias project" {
		t.Fatalf("requestProjectHeader = %q; want %q", got, "alias project")
	}
}

func TestRequestProjectHeader_AbsentReturnsEmpty(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	if got := requestProjectHeader(req); got != "" {
		t.Fatalf("requestProjectHeader = %q; want empty", got)
	}
}

// ─── Bearer token extraction ───────────────────────────────────────────────

func TestBearerToken_ExtractsFromAuthorizationHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer abc123")
	if got := bearerToken(req); got != "abc123" {
		t.Fatalf("bearerToken = %q; want %q", got, "abc123")
	}
}

func TestBearerToken_AbsentOrMalformedReturnsEmpty(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	if got := bearerToken(req); got != "" {
		t.Fatalf("bearerToken (absent) = %q; want empty", got)
	}
	req.Header.Set("Authorization", "Basic abc123")
	if got := bearerToken(req); got != "" {
		t.Fatalf("bearerToken (non-bearer scheme) = %q; want empty", got)
	}
}

// ─── Local bearer guard: 401 vs pass-through ──────────────────────────────

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestWithLocalBearerGuard_NoTokenConfiguredIsNoOp(t *testing.T) {
	srv := httptest.NewServer(withLocalBearerGuard(okHandler(), ""))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/mcp")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200 (no guard configured)", resp.StatusCode)
	}
}

func TestWithLocalBearerGuard_MatchingTokenPasses(t *testing.T) {
	srv := httptest.NewServer(withLocalBearerGuard(okHandler(), "expected-token"))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/mcp", nil)
	req.Header.Set("Authorization", "Bearer expected-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200 (matching token)", resp.StatusCode)
	}
}

func TestWithLocalBearerGuard_MismatchedOrMissingTokenRejected(t *testing.T) {
	srv := httptest.NewServer(withLocalBearerGuard(okHandler(), "expected-token"))
	defer srv.Close()

	// Missing Authorization header entirely.
	resp, err := http.Get(srv.URL + "/mcp")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status (missing token) = %d; want 401", resp.StatusCode)
	}

	// Wrong token.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/mcp", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status (wrong token) = %d; want 401", resp2.StatusCode)
	}
}

func TestWithLocalBearerGuard_HealthExemptFromGuard(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", handleHealth)
	mux.Handle("/mcp", okHandler())
	srv := httptest.NewServer(withLocalBearerGuard(mux, "expected-token"))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/health status = %d; want 200 even without a token", resp.StatusCode)
	}
}

// ─── Loopback detection for the non-loopback startup warning ─────────────

func TestIsLoopbackAddr(t *testing.T) {
	tests := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:7438", true},
		{"localhost:7438", true},
		{"[::1]:7438", true},
		{":7438", true},
		{"0.0.0.0:7438", false},
		{"192.168.1.5:7438", false},
	}
	for _, tt := range tests {
		if got := isLoopbackAddr(tt.addr); got != tt.want {
			t.Errorf("isLoopbackAddr(%q) = %v; want %v", tt.addr, got, tt.want)
		}
	}
}

// TestWithOriginHostGuard covers cross-origin and DNS-rebinding rejection.
func TestWithOriginHostGuard(t *testing.T) {
	tests := []struct {
		name   string
		cfg    HTTPTransportConfig
		origin string
		host   string // empty keeps httptest's own loopback Host
		want   int
	}{
		{"foreign origin rejected", HTTPTransportConfig{}, "https://evil.example", "", http.StatusForbidden},
		{"allowlisted origin passes", HTTPTransportConfig{AllowedOrigins: "https://good.example"}, "https://good.example", "", http.StatusOK},
		{"no token, foreign host rejected", HTTPTransportConfig{}, "", "evil.example:7438", http.StatusForbidden},
		{"no token, loopback host passes", HTTPTransportConfig{}, "", "", http.StatusOK},
		{"no token, allowlisted host passes", HTTPTransportConfig{AllowedHosts: "evil.example"}, "", "evil.example:7438", http.StatusOK},
		{"token configured skips host check", HTTPTransportConfig{LocalToken: "secret"}, "", "evil.example:7438", http.StatusOK},
	}
	for _, tt := range tests {
		srv := httptest.NewServer(withOriginHostGuard(withLocalBearerGuard(okHandler(), tt.cfg.LocalToken), tt.cfg))
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", nil)
		if tt.origin != "" {
			req.Header.Set("Origin", tt.origin)
		}
		if tt.host != "" {
			req.Host = tt.host
		}
		if tt.cfg.LocalToken != "" {
			req.Header.Set("Authorization", "Bearer "+tt.cfg.LocalToken)
		}
		resp, err := http.DefaultClient.Do(req)
		srv.Close()
		if err != nil {
			t.Fatalf("%s: POST: %v", tt.name, err)
		}
		resp.Body.Close()
		if resp.StatusCode != tt.want {
			t.Fatalf("%s: status = %d; want %d", tt.name, resp.StatusCode, tt.want)
		}
	}
}

// ─── Cloud bearer guard (T2): delegates to a CloudBearerAuthenticator ─────

// fakeCloudAuth is a test double for CloudBearerAuthenticator that records
// the token/project it was called with and returns a scripted result.
type fakeCloudAuth struct {
	result   bool
	err      error
	called   bool
	gotToken string
	gotProj  string
}

func (f *fakeCloudAuth) Authenticate(_ context.Context, token, project string) (bool, error) {
	f.called = true
	f.gotToken = token
	f.gotProj = project
	return f.result, f.err
}

func TestWithCloudBearerGuard_ValidBearerPasses(t *testing.T) {
	auth := &fakeCloudAuth{result: true}
	srv := httptest.NewServer(withCloudBearerGuard(okHandler(), auth))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", nil)
	req.Header.Set("Authorization", "Bearer cloud-token")
	req.Header.Set("X-Engram-Subproject", "demo")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200", resp.StatusCode)
	}
	if !auth.called || auth.gotToken != "cloud-token" || auth.gotProj != "demo" {
		t.Fatalf("Authenticate called=%v token=%q project=%q; want called with cloud-token/demo", auth.called, auth.gotToken, auth.gotProj)
	}
}

func TestWithCloudBearerGuard_InvalidBearerRejectedWith401(t *testing.T) {
	auth := &fakeCloudAuth{result: false}
	srv := httptest.NewServer(withCloudBearerGuard(okHandler(), auth))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d; want 401", resp.StatusCode)
	}
}

func TestWithCloudBearerGuard_UnreachableCloudRejectedWith503(t *testing.T) {
	auth := &fakeCloudAuth{err: errors.New("cloud unreachable")}
	srv := httptest.NewServer(withCloudBearerGuard(okHandler(), auth))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", nil)
	req.Header.Set("Authorization", "Bearer some-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; want 503", resp.StatusCode)
	}
}

func TestWithCloudBearerGuard_HealthExemptFromGuard(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", handleHealth)
	mux.Handle("/mcp", okHandler())
	auth := &fakeCloudAuth{result: false}
	srv := httptest.NewServer(withCloudBearerGuard(mux, auth))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/health status = %d; want 200 even when the cloud authenticator would reject", resp.StatusCode)
	}
	if auth.called {
		t.Fatal("Authenticate must not be called for /health")
	}
}

func TestNewHTTPHandler_CloudAuthTakesOverFromLocalToken(t *testing.T) {
	s := newMCPTestStore(t)
	mcpSrv := NewServerWithConfig(s, MCPConfig{}, nil)
	auth := &fakeCloudAuth{result: true}
	// LocalToken set alongside CloudAuth: the caller is responsible for
	// enforcing mutual exclusivity at startup, but the transport itself
	// must deterministically prefer CloudAuth so a request with no
	// LocalToken-matching header still passes through cloud auth.
	handler := NewHTTPHandler(mcpSrv, HTTPTransportConfig{LocalToken: "local-secret", CloudAuth: auth})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", nil)
	req.Header.Set("Authorization", "Bearer not-the-local-secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		t.Fatal("expected CloudAuth to be consulted instead of the LocalToken guard")
	}
	if !auth.called {
		t.Fatal("expected CloudAuth.Authenticate to be called")
	}
}

func TestWithOriginHostGuard_CloudAuthSkipsHostCheck(t *testing.T) {
	auth := &fakeCloudAuth{result: true}
	cfg := HTTPTransportConfig{CloudAuth: auth}
	srv := httptest.NewServer(withOriginHostGuard(withCloudBearerGuard(okHandler(), auth), cfg))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", nil)
	req.Host = "evil.example:7438"
	req.Header.Set("Authorization", "Bearer some-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200 (a configured CloudAuth already defeats rebinding, like LocalToken)", resp.StatusCode)
	}
}

// A bearer-less request in cloud mode rides the .env fallback token, so it
// carries no secret and must still pass the Host check.
func TestWithOriginHostGuard_CloudAuthWithoutBearerKeepsHostCheck(t *testing.T) {
	auth := &fakeCloudAuth{result: true}
	cfg := HTTPTransportConfig{CloudAuth: auth}
	srv := httptest.NewServer(withOriginHostGuard(withCloudBearerGuard(okHandler(), auth), cfg))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", nil)
	req.Host = "evil.example:7438"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d; want 403 (no bearer means no secret defeats rebinding)", resp.StatusCode)
	}
}

// ─── GET /health via the full handler ─────────────────────────────────────

func TestNewHTTPHandler_Health(t *testing.T) {
	s := newMCPTestStore(t)
	mcpSrv := NewServerWithConfig(s, MCPConfig{}, nil)
	handler := NewHTTPHandler(mcpSrv, HTTPTransportConfig{})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200", resp.StatusCode)
	}
}

// ─── End-to-end: initialize + tools/call mem_save then mem_search over
// streamable HTTP, using only the X-Engram-Subproject header (no explicit
// project tool argument) to prove header-driven project resolution. ───────

func TestHTTPTransport_SaveThenSearchUsesHeaderProject(t *testing.T) {
	s := newMCPTestStore(t)
	mcpSrv := NewServerWithConfig(s, MCPConfig{}, nil)
	handler := NewHTTPHandler(mcpSrv, HTTPTransportConfig{})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	headers := map[string]string{
		"X-Engram-Subproject": "http e2e project",
	}
	c, err := mcpclient.NewStreamableHttpClient(srv.URL+"/mcp", transport.WithHTTPHeaders(headers))
	if err != nil {
		t.Fatalf("new streamable http client: %v", err)
	}
	defer c.Close()

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	initReq := mcppkg.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcppkg.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcppkg.Implementation{Name: "engram-http-e2e-test", Version: "0.0.0"}
	if _, err := c.Initialize(ctx, initReq); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	saveReq := mcppkg.CallToolRequest{}
	saveReq.Params.Name = "mem_save"
	saveReq.Params.Arguments = map[string]any{
		"title":   "HTTP transport e2e",
		"content": "Saved over streamable HTTP using only the subproject header.",
	}
	saveRes, err := c.CallTool(ctx, saveReq)
	if err != nil {
		t.Fatalf("call mem_save: %v", err)
	}
	if saveRes.IsError {
		t.Fatalf("mem_save returned an error result: %+v", saveRes.Content)
	}

	searchReq := mcppkg.CallToolRequest{}
	searchReq.Params.Name = "mem_search"
	searchReq.Params.Arguments = map[string]any{
		"query": "streamable HTTP",
	}
	searchRes, err := c.CallTool(ctx, searchReq)
	if err != nil {
		t.Fatalf("call mem_search: %v", err)
	}
	if searchRes.IsError {
		t.Fatalf("mem_search returned an error result: %+v", searchRes.Content)
	}

	text, ok := mcppkg.AsTextContent(searchRes.Content[0])
	if !ok {
		t.Fatalf("expected text content in mem_search result")
	}
	if !strings.Contains(text.Text, "HTTP transport e2e") {
		t.Fatalf("mem_search result missing saved observation, got: %s", text.Text)
	}

	// Confirm the observation actually landed under the header project, not
	// some cwd-detected or default bucket.
	obs, err := s.RecentObservations("http e2e project", "project", 5)
	if err != nil {
		t.Fatalf("recent observations: %v", err)
	}
	if len(obs) == 0 {
		t.Fatal("expected the observation to be stored under the header project")
	}
}

// TestHTTPTransport_SaveWithoutProjectSignalErrorsAndPersistsNothing covers mem_save over HTTP with no project signal.
func TestHTTPTransport_SaveWithoutProjectSignalErrorsAndPersistsNothing(t *testing.T) {
	s := newMCPTestStore(t)
	mcpSrv := NewServerWithConfig(s, MCPConfig{}, nil)
	handler := NewHTTPHandler(mcpSrv, HTTPTransportConfig{})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	c, err := mcpclient.NewStreamableHttpClient(srv.URL + "/mcp")
	if err != nil {
		t.Fatalf("new streamable http client: %v", err)
	}
	defer c.Close()
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	initReq := mcppkg.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcppkg.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcppkg.Implementation{Name: "engram-http-e2e-test", Version: "0.0.0"}
	if _, err := c.Initialize(ctx, initReq); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	saveReq := mcppkg.CallToolRequest{}
	saveReq.Params.Name = "mem_save"
	saveReq.Params.Arguments = map[string]any{
		"title":   "should never persist",
		"content": "no project header, no default project, no ENGRAM_PROJECT",
	}
	saveRes, err := c.CallTool(ctx, saveReq)
	if err != nil {
		t.Fatalf("call mem_save: %v", err)
	}
	text, ok := mcppkg.AsTextContent(saveRes.Content[0])
	if !saveRes.IsError || !ok || !strings.Contains(text.Text, "X-Engram-Subproject") {
		t.Fatalf("expected mem_save to fail closed mentioning X-Engram-Subproject, got: %+v", saveRes.Content)
	}
	if count, err := s.CountObservationsForProject(""); err != nil || count != 0 {
		t.Fatalf("expected no observation persisted under an empty project, got count=%d err=%v", count, err)
	}
}

package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Gentleman-Programming/engram/v2/internal/cloud/autosync"
	"github.com/Gentleman-Programming/engram/v2/internal/mcp"
	"github.com/Gentleman-Programming/engram/v2/internal/store"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// ─── validateMCPHTTPAuthConfig: one Authorization slot, one meaning ───────

func TestValidateMCPHTTPAuthConfig(t *testing.T) {
	tests := []struct {
		name           string
		localToken     string
		cloudRequested bool
		wantErr        bool
	}{
		{"neither configured", "", false, false},
		{"local token only", "secret", false, false},
		{"cloud mode only", "", true, false},
		{"both configured is an error", "secret", true, true},
		{"whitespace-only local token with cloud mode is not both", "   ", true, false},
	}
	for _, tt := range tests {
		err := validateMCPHTTPAuthConfig(tt.localToken, tt.cloudRequested)
		if (err != nil) != tt.wantErr {
			t.Errorf("%s: validateMCPHTTPAuthConfig(%q, %v) error = %v; wantErr=%v", tt.name, tt.localToken, tt.cloudRequested, err, tt.wantErr)
		}
	}
}

// ─── cmdMCP wiring ─────────────────────────────────────────────────────────

func TestCmdMCPHTTP_BothTokenModesIsStartupError(t *testing.T) {
	cfg := testConfig(t)
	stubRuntimeHooks(t)
	stubExitWithPanic(t)

	t.Setenv(mcp.EnvHTTPToken, "local-secret")
	t.Setenv("ENGRAM_CLOUD_AUTOSYNC", "1")

	withArgs(t, "engram", "mcp", "--transport=http")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdMCP(cfg) })
	code, ok := recovered.(exitCode)
	if !ok || int(code) != 1 {
		t.Fatalf("expected exit code 1, got %v (stderr=%q)", recovered, stderr)
	}
	if !strings.Contains(stderr, mcp.EnvHTTPToken) || !strings.Contains(stderr, "ENGRAM_CLOUD_AUTOSYNC") {
		t.Fatalf("expected stderr to mention both env vars, got %q", stderr)
	}
}

func TestCmdMCPHTTP_LocalOnlyModeHasNoCloudAuth(t *testing.T) {
	cfg := testConfig(t)
	stubRuntimeHooks(t)
	stubExitWithPanic(t)

	oldNewMCPServerWithConfig := newMCPServerWithConfig
	t.Cleanup(func() { newMCPServerWithConfig = oldNewMCPServerWithConfig })
	newMCPServerWithConfig = func(s *store.Store, mcpCfg mcp.MCPConfig, allowlist map[string]bool) *mcpserver.MCPServer {
		return mcpserver.NewMCPServer("test", "0")
	}

	var gotCfg mcp.HTTPTransportConfig
	oldServeMCPHTTP := serveMCPHTTP
	t.Cleanup(func() { serveMCPHTTP = oldServeMCPHTTP })
	serveMCPHTTP = func(_ context.Context, _ *mcpserver.MCPServer, cfg mcp.HTTPTransportConfig) error {
		gotCfg = cfg
		return nil
	}

	t.Setenv("ENGRAM_CLOUD_AUTOSYNC", "")
	t.Setenv(mcp.EnvHTTPToken, "local-secret")

	withArgs(t, "engram", "mcp", "--transport=http")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdMCP(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("expected clean run, got panic=%v stderr=%q", recovered, stderr)
	}
	if gotCfg.CloudAuth != nil {
		t.Fatal("expected CloudAuth to stay nil in local-only mode (T1 behavior unchanged)")
	}
	if gotCfg.LocalToken != "local-secret" {
		t.Fatalf("LocalToken = %q; want %q", gotCfg.LocalToken, "local-secret")
	}
}

func TestCmdMCPHTTP_CloudModeWiresCloudAuth(t *testing.T) {
	cfg := testConfig(t)
	stubRuntimeHooks(t)
	stubExitWithPanic(t)

	oldNewMCPServerWithConfig := newMCPServerWithConfig
	t.Cleanup(func() { newMCPServerWithConfig = oldNewMCPServerWithConfig })
	newMCPServerWithConfig = func(s *store.Store, mcpCfg mcp.MCPConfig, allowlist map[string]bool) *mcpserver.MCPServer {
		return mcpserver.NewMCPServer("test", "0")
	}

	var gotCfg mcp.HTTPTransportConfig
	oldServeMCPHTTP := serveMCPHTTP
	t.Cleanup(func() { serveMCPHTTP = oldServeMCPHTTP })
	serveMCPHTTP = func(_ context.Context, _ *mcpserver.MCPServer, cfg mcp.HTTPTransportConfig) error {
		gotCfg = cfg
		return nil
	}

	old := newAutosyncManager
	newAutosyncManager = func(_ *store.Store, _ autosync.CloudTransport, _ autosync.Config) startableAutosyncManager {
		return &fakeStartableManager{}
	}
	t.Cleanup(func() { newAutosyncManager = old })

	t.Setenv("ENGRAM_CLOUD_AUTOSYNC", "1")
	t.Setenv(mcp.EnvHTTPToken, "")
	t.Setenv("ENGRAM_CLOUD_SERVER", "https://cloud.example.test")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "cloud-token")

	withArgs(t, "engram", "mcp", "--transport=http")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdMCP(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("expected clean run, got panic=%v stderr=%q", recovered, stderr)
	}
	if gotCfg.CloudAuth == nil {
		t.Fatal("expected HTTPTransportConfig.CloudAuth to be wired in cloud mode")
	}
}

func TestCmdMCPHTTP_CloudModeMisconfiguredFailsStartup(t *testing.T) {
	cfg := testConfig(t)
	stubRuntimeHooks(t)
	stubExitWithPanic(t)

	oldNewMCPServerWithConfig := newMCPServerWithConfig
	t.Cleanup(func() { newMCPServerWithConfig = oldNewMCPServerWithConfig })
	newMCPServerWithConfig = func(s *store.Store, mcpCfg mcp.MCPConfig, allowlist map[string]bool) *mcpserver.MCPServer {
		return mcpserver.NewMCPServer("test", "0")
	}

	t.Setenv("ENGRAM_CLOUD_AUTOSYNC", "1")
	t.Setenv(mcp.EnvHTTPToken, "")
	// No server URL / token resolvable anywhere (env or cloud.json): autosync
	// cannot start, so cloud mode must fail fast at startup rather than
	// silently serving an unauthenticated endpoint.
	t.Setenv("ENGRAM_CLOUD_SERVER", "")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "")

	withArgs(t, "engram", "mcp", "--transport=http")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdMCP(cfg) })
	code, ok := recovered.(exitCode)
	if !ok || int(code) != 1 {
		t.Fatalf("expected exit code 1, got %v (stderr=%q)", recovered, stderr)
	}
	if !strings.Contains(stderr, "ENGRAM_CLOUD_AUTOSYNC") {
		t.Fatalf("expected stderr to explain the cloud misconfiguration, got %q", stderr)
	}
}

// TestCmdMCPHTTPGracefulShutdownStopsAutosync asserts that when
// serveMCPHTTP returns (a completed graceful shutdown), cmdMCP's deferred
// stopAutosync has actually drained the autosync manager — addressing
// advisory R3-http-shutdown-autosync from the T1 review.
func TestCmdMCPHTTPGracefulShutdownStopsAutosync(t *testing.T) {
	cfg := testConfig(t)
	stubRuntimeHooks(t)
	stubExitWithPanic(t)

	oldNewMCPServerWithConfig := newMCPServerWithConfig
	t.Cleanup(func() { newMCPServerWithConfig = oldNewMCPServerWithConfig })
	newMCPServerWithConfig = func(s *store.Store, mcpCfg mcp.MCPConfig, allowlist map[string]bool) *mcpserver.MCPServer {
		return mcpserver.NewMCPServer("test", "0")
	}

	oldServeMCPHTTP := serveMCPHTTP
	t.Cleanup(func() { serveMCPHTTP = oldServeMCPHTTP })
	serveMCPHTTP = func(_ context.Context, _ *mcpserver.MCPServer, _ mcp.HTTPTransportConfig) error {
		return nil // simulate a graceful shutdown that already completed
	}

	stopped := false
	old := newAutosyncManager
	newAutosyncManager = func(_ *store.Store, _ autosync.CloudTransport, _ autosync.Config) startableAutosyncManager {
		return &fakeStartableManager{stopFn: func() { stopped = true }}
	}
	t.Cleanup(func() { newAutosyncManager = old })

	t.Setenv("ENGRAM_CLOUD_AUTOSYNC", "1")
	t.Setenv(mcp.EnvHTTPToken, "")
	t.Setenv("ENGRAM_CLOUD_SERVER", "https://cloud.example.test")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "cloud-token")

	withArgs(t, "engram", "mcp", "--transport=http")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdMCP(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("expected clean run, got panic=%v stderr=%q", recovered, stderr)
	}
	if !stopped {
		t.Fatal("expected autosync manager Stop to be called once serveMCPHTTP returns (graceful shutdown must drain autosync)")
	}
}

// ─── T5: bearer-only cloud mode (no ENGRAM_CLOUD_TOKEN configured) ────────

// setupLazyCloudTestServer wires cmdMCP with a fake HTTP transport and fake
// autosync factory, returning the captured HTTPTransportConfig and a
// channel that receives one value each time the autosync manager's run loop
// actually starts (fakeStartableManager without a Start handshake launches
// Run in its own goroutine — see tryStartAutosync/startAutosyncManager — so
// tests must synchronize on this channel rather than poll an atomic
// counter immediately after Authenticate returns).
func setupLazyCloudTestServer(t *testing.T) (*mcp.HTTPTransportConfig, chan struct{}) {
	t.Helper()

	oldNewMCPServerWithConfig := newMCPServerWithConfig
	t.Cleanup(func() { newMCPServerWithConfig = oldNewMCPServerWithConfig })
	newMCPServerWithConfig = func(s *store.Store, mcpCfg mcp.MCPConfig, allowlist map[string]bool) *mcpserver.MCPServer {
		return mcpserver.NewMCPServer("test", "0")
	}

	runStarted := make(chan struct{}, 32)
	old := newAutosyncManager
	newAutosyncManager = func(_ *store.Store, _ autosync.CloudTransport, _ autosync.Config) startableAutosyncManager {
		return &fakeStartableManager{
			runFn: func(context.Context) { runStarted <- struct{}{} },
		}
	}
	t.Cleanup(func() { newAutosyncManager = old })

	gotCfg := &mcp.HTTPTransportConfig{}
	oldServeMCPHTTP := serveMCPHTTP
	t.Cleanup(func() { serveMCPHTTP = oldServeMCPHTTP })
	serveMCPHTTP = func(_ context.Context, _ *mcpserver.MCPServer, cfg mcp.HTTPTransportConfig) error {
		*gotCfg = cfg
		return nil
	}

	return gotCfg, runStarted
}

// assertNoRunSignal fails the test if runStarted has already delivered (or
// delivers within a short grace window is NOT what this checks — it is a
// deterministic, non-blocking check, safe to use only where an additional
// signal is structurally impossible, e.g. after sync.Once has already fired
// or when the authenticator never called the lazy-start hook).
func assertNoRunSignal(t *testing.T, runStarted chan struct{}, msg string) {
	t.Helper()
	select {
	case <-runStarted:
		t.Fatal(msg)
	default:
	}
}

// TestCmdMCPHTTP_CloudModeNoTokenStartsWithoutFatal is the core T5
// requirement: ENGRAM_CLOUD_AUTOSYNC=1 with a valid ENGRAM_CLOUD_SERVER but
// no ENGRAM_CLOUD_TOKEN (env or cloud.json) must not fatal startup, and
// autosync must not have started before any bearer was validated.
func TestCmdMCPHTTP_CloudModeNoTokenStartsWithoutFatal(t *testing.T) {
	cfg := testConfig(t)
	stubRuntimeHooks(t)
	stubExitWithPanic(t)
	gotCfg, runStarted := setupLazyCloudTestServer(t)

	t.Setenv("ENGRAM_CLOUD_AUTOSYNC", "1")
	t.Setenv(mcp.EnvHTTPToken, "")
	t.Setenv("ENGRAM_CLOUD_SERVER", "https://cloud.example.test")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "")

	withArgs(t, "engram", "mcp", "--transport=http")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdMCP(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("expected clean run (no fatal) with server-only cloud config, got panic=%v stderr=%q", recovered, stderr)
	}
	if gotCfg.CloudAuth == nil {
		t.Fatal("expected CloudAuth to be wired even without ENGRAM_CLOUD_TOKEN")
	}
	// No request was ever authenticated, so the lazy-start hook was never
	// invoked: no goroutine exists that could ever send on runStarted, so a
	// non-blocking check here is deterministic, not racy.
	assertNoRunSignal(t, runStarted, "expected autosync NOT to start before any authenticated request")
}

// TestCmdMCPHTTP_CloudModeNoTokenFirstBearerStartsAutosyncOnce covers the
// lazy-start hook: the first bearer the authenticator accepts (owner
// pinned) must start autosync with that token exactly once, even under
// concurrent first requests (see -race variant below).
func TestCmdMCPHTTP_CloudModeNoTokenFirstBearerStartsAutosyncOnce(t *testing.T) {
	cfg := testConfig(t)
	stubRuntimeHooks(t)
	stubExitWithPanic(t)
	gotCfg, runStarted := setupLazyCloudTestServer(t)

	t.Setenv("ENGRAM_CLOUD_AUTOSYNC", "1")
	t.Setenv(mcp.EnvHTTPToken, "")
	t.Setenv("ENGRAM_CLOUD_SERVER", "https://cloud.example.test")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "")

	withArgs(t, "engram", "mcp", "--transport=http")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdMCP(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("expected clean run, got panic=%v stderr=%q", recovered, stderr)
	}
	authImpl, ok := gotCfg.CloudAuth.(*cloudBearerAuthenticator)
	if !ok {
		t.Fatalf("expected CloudAuth to be a *cloudBearerAuthenticator, got %T", gotCfg.CloudAuth)
	}
	authImpl.validate = func(_, _ string) (string, error) { return "acct-1", nil }

	okAuth, err := authImpl.Authenticate(context.Background(), "bearer-1", "demo")
	if err != nil || !okAuth {
		t.Fatalf("Authenticate = (%v, %v); want (true, nil)", okAuth, err)
	}
	waitForSignal(t, runStarted, "autosync run to start after the first validated bearer")
	assertNoRunSignal(t, runStarted, "expected autosync to start exactly once after the first validated bearer")

	// A second validated bearer from the same (pinned) owner must not start
	// autosync a second time: sync.Once already fired, so no goroutine can
	// ever send on runStarted again — the check below is deterministic.
	okAuth, err = authImpl.Authenticate(context.Background(), "bearer-1-again", "demo")
	if err != nil || !okAuth {
		t.Fatalf("Authenticate = (%v, %v); want (true, nil) for a second same-owner bearer", okAuth, err)
	}
	assertNoRunSignal(t, runStarted, "expected autosync start to remain exactly once")
}

// TestCmdMCPHTTP_CloudModeNoTokenConcurrentFirstRequestsStartOnce is the
// -race regression: concurrent first requests must start autosync exactly
// once (sync.Once / equivalent), never twice and never with a data race.
func TestCmdMCPHTTP_CloudModeNoTokenConcurrentFirstRequestsStartOnce(t *testing.T) {
	cfg := testConfig(t)
	stubRuntimeHooks(t)
	stubExitWithPanic(t)
	gotCfg, runStarted := setupLazyCloudTestServer(t)

	t.Setenv("ENGRAM_CLOUD_AUTOSYNC", "1")
	t.Setenv(mcp.EnvHTTPToken, "")
	t.Setenv("ENGRAM_CLOUD_SERVER", "https://cloud.example.test")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "")

	withArgs(t, "engram", "mcp", "--transport=http")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdMCP(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("expected clean run, got panic=%v stderr=%q", recovered, stderr)
	}
	authImpl, ok := gotCfg.CloudAuth.(*cloudBearerAuthenticator)
	if !ok {
		t.Fatalf("expected CloudAuth to be a *cloudBearerAuthenticator, got %T", gotCfg.CloudAuth)
	}
	authImpl.validate = func(_, _ string) (string, error) { return "acct-1", nil }

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			okAuth, err := authImpl.Authenticate(context.Background(), fmt.Sprintf("bearer-%d", i), "demo")
			if err != nil || !okAuth {
				t.Errorf("Authenticate(bearer-%d) = (%v, %v); want (true, nil)", i, okAuth, err)
			}
		}(i)
	}
	wg.Wait()

	waitForSignal(t, runStarted, "autosync run to start under concurrent first requests")
	assertNoRunSignal(t, runStarted, "expected exactly one autosync start under concurrent first requests")
}

// TestCmdMCPHTTP_CloudModeNoTokenDifferentAccountRejectedNoStart asserts
// that a bearer belonging to a different cloud account never starts
// autosync and never overrides the pinned owner's token.
func TestCmdMCPHTTP_CloudModeNoTokenDifferentAccountRejectedNoStart(t *testing.T) {
	cfg := testConfig(t)
	stubRuntimeHooks(t)
	stubExitWithPanic(t)
	gotCfg, runStarted := setupLazyCloudTestServer(t)

	t.Setenv("ENGRAM_CLOUD_AUTOSYNC", "1")
	t.Setenv(mcp.EnvHTTPToken, "")
	t.Setenv("ENGRAM_CLOUD_SERVER", "https://cloud.example.test")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "")

	withArgs(t, "engram", "mcp", "--transport=http")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdMCP(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("expected clean run, got panic=%v stderr=%q", recovered, stderr)
	}
	authImpl, ok := gotCfg.CloudAuth.(*cloudBearerAuthenticator)
	if !ok {
		t.Fatalf("expected CloudAuth to be a *cloudBearerAuthenticator, got %T", gotCfg.CloudAuth)
	}
	principals := map[string]string{"owner-token": "acct-owner", "attacker-token": "acct-attacker"}
	authImpl.validate = func(_, token string) (string, error) { return principals[token], nil }

	okAuth, err := authImpl.Authenticate(context.Background(), "owner-token", "demo")
	if err != nil || !okAuth {
		t.Fatalf("Authenticate(owner-token) = (%v, %v); want (true, nil)", okAuth, err)
	}
	waitForSignal(t, runStarted, "owner bearer to start autosync")

	okAuth, err = authImpl.Authenticate(context.Background(), "attacker-token", "demo")
	if err != nil || okAuth {
		t.Fatalf("Authenticate(attacker-token) = (%v, %v); want (false, nil): different account must be rejected", okAuth, err)
	}
	// A rejected bearer never reaches the setSyncToken/lazy-start hook, so no
	// goroutine can ever send on runStarted again — deterministic check.
	assertNoRunSignal(t, runStarted, "expected no additional autosync start for a rejected bearer")
}

// TestCmdMCPHTTP_CloudModeNoServerFailsEvenWithToken asserts that a missing
// ENGRAM_CLOUD_SERVER is still a fatal startup error in HTTP mode, even when
// ENGRAM_CLOUD_TOKEN is configured (T5 must not weaken this existing
// REQ-211 server requirement).
func TestCmdMCPHTTP_CloudModeNoServerFailsEvenWithToken(t *testing.T) {
	cfg := testConfig(t)
	stubRuntimeHooks(t)
	stubExitWithPanic(t)

	oldNewMCPServerWithConfig := newMCPServerWithConfig
	t.Cleanup(func() { newMCPServerWithConfig = oldNewMCPServerWithConfig })
	newMCPServerWithConfig = func(s *store.Store, mcpCfg mcp.MCPConfig, allowlist map[string]bool) *mcpserver.MCPServer {
		return mcpserver.NewMCPServer("test", "0")
	}

	t.Setenv("ENGRAM_CLOUD_AUTOSYNC", "1")
	t.Setenv(mcp.EnvHTTPToken, "")
	t.Setenv("ENGRAM_CLOUD_SERVER", "")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "cloud-token")

	withArgs(t, "engram", "mcp", "--transport=http")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdMCP(cfg) })
	code, ok := recovered.(exitCode)
	if !ok || int(code) != 1 {
		t.Fatalf("expected exit code 1, got %v (stderr=%q)", recovered, stderr)
	}
	if !strings.Contains(stderr, "ENGRAM_CLOUD_SERVER") {
		t.Fatalf("expected stderr to explain the missing ENGRAM_CLOUD_SERVER, got %q", stderr)
	}
}

// TestCmdMCPHTTP_CloudModeNonHTTPSServerFailsStartup (T7): the HTTP
// transport's cloud auth path always sends the request bearer to
// ENGRAM_CLOUD_SERVER (see cloudBearerAuthenticator.Authenticate ->
// remote.ValidateBearer), and internal/cloud/remote refuses to send any
// bearer over plaintext HTTP. Before T7, that refusal only surfaced per
// request as a 503 once a client connected. Startup must fail fast instead,
// even in bearer-only mode (no ENGRAM_CLOUD_TOKEN configured).
func TestCmdMCPHTTP_CloudModeNonHTTPSServerFailsStartup(t *testing.T) {
	cfg := testConfig(t)
	stubRuntimeHooks(t)
	stubExitWithPanic(t)

	oldNewMCPServerWithConfig := newMCPServerWithConfig
	t.Cleanup(func() { newMCPServerWithConfig = oldNewMCPServerWithConfig })
	newMCPServerWithConfig = func(s *store.Store, mcpCfg mcp.MCPConfig, allowlist map[string]bool) *mcpserver.MCPServer {
		return mcpserver.NewMCPServer("test", "0")
	}

	t.Setenv("ENGRAM_CLOUD_AUTOSYNC", "1")
	t.Setenv(mcp.EnvHTTPToken, "")
	t.Setenv("ENGRAM_CLOUD_SERVER", "http://host.docker.internal:18080")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "")

	withArgs(t, "engram", "mcp", "--transport=http")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdMCP(cfg) })
	code, ok := recovered.(exitCode)
	if !ok || int(code) != 1 {
		t.Fatalf("expected exit code 1, got %v (stderr=%q)", recovered, stderr)
	}
	if !strings.Contains(stderr, "ENGRAM_CLOUD_SERVER") || !strings.Contains(stderr, "HTTPS") {
		t.Fatalf("expected stderr to explain the non-HTTPS ENGRAM_CLOUD_SERVER, got %q", stderr)
	}
}

// TestCmdMCPHTTPGracefulShutdownStopsLazilyStartedAutosync asserts that when
// autosync started lazily (T5, no ENGRAM_CLOUD_TOKEN) after a request, a
// later graceful shutdown still stops it — no leak, no double stop.
func TestCmdMCPHTTPGracefulShutdownStopsLazilyStartedAutosync(t *testing.T) {
	cfg := testConfig(t)
	stubRuntimeHooks(t)
	stubExitWithPanic(t)

	oldNewMCPServerWithConfig := newMCPServerWithConfig
	t.Cleanup(func() { newMCPServerWithConfig = oldNewMCPServerWithConfig })
	newMCPServerWithConfig = func(s *store.Store, mcpCfg mcp.MCPConfig, allowlist map[string]bool) *mcpserver.MCPServer {
		return mcpserver.NewMCPServer("test", "0")
	}

	var stopCalls int32
	old := newAutosyncManager
	newAutosyncManager = func(_ *store.Store, _ autosync.CloudTransport, _ autosync.Config) startableAutosyncManager {
		return &fakeStartableManager{stopFn: func() { atomic.AddInt32(&stopCalls, 1) }}
	}
	t.Cleanup(func() { newAutosyncManager = old })

	var gotCfg mcp.HTTPTransportConfig
	oldServeMCPHTTP := serveMCPHTTP
	t.Cleanup(func() { serveMCPHTTP = oldServeMCPHTTP })
	serveMCPHTTP = func(_ context.Context, _ *mcpserver.MCPServer, cfg mcp.HTTPTransportConfig) error {
		gotCfg = cfg
		authImpl := cfg.CloudAuth.(*cloudBearerAuthenticator)
		authImpl.validate = func(_, _ string) (string, error) { return "acct-1", nil }
		if _, err := authImpl.Authenticate(context.Background(), "bearer-1", "demo"); err != nil {
			t.Fatalf("Authenticate: %v", err)
		}
		return nil // simulate a graceful shutdown after the first request
	}

	t.Setenv("ENGRAM_CLOUD_AUTOSYNC", "1")
	t.Setenv(mcp.EnvHTTPToken, "")
	t.Setenv("ENGRAM_CLOUD_SERVER", "https://cloud.example.test")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "")

	withArgs(t, "engram", "mcp", "--transport=http")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdMCP(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("expected clean run, got panic=%v stderr=%q", recovered, stderr)
	}
	if gotCfg.CloudAuth == nil {
		t.Fatal("expected CloudAuth to be wired")
	}
	if got := atomic.LoadInt32(&stopCalls); got != 1 {
		t.Fatalf("expected the lazily-started autosync manager to be stopped exactly once, got %d", got)
	}
}

// TestCmdMCPHTTP_CloudModeHTTPSServerStarts (R3-missing-positive-startup-test):
// the positive counterpart of the non-HTTPS startup failure — an https
// ENGRAM_CLOUD_SERVER with no token starts cleanly in bearer-only mode.
// It also asserts the boot log is the informational bearer-only line, not the
// stdio REQ-211 "token is not configured" ERROR.
func TestCmdMCPHTTP_CloudModeHTTPSServerStarts(t *testing.T) {
	cfg := testConfig(t)
	stubRuntimeHooks(t)
	stubExitWithPanic(t)

	oldNewMCPServerWithConfig := newMCPServerWithConfig
	t.Cleanup(func() { newMCPServerWithConfig = oldNewMCPServerWithConfig })
	newMCPServerWithConfig = func(s *store.Store, mcpCfg mcp.MCPConfig, allowlist map[string]bool) *mcpserver.MCPServer {
		return mcpserver.NewMCPServer("test", "0")
	}
	served := false
	oldServeMCPHTTP := serveMCPHTTP
	t.Cleanup(func() { serveMCPHTTP = oldServeMCPHTTP })
	serveMCPHTTP = func(_ context.Context, _ *mcpserver.MCPServer, c mcp.HTTPTransportConfig) error {
		served = c.CloudAuth != nil
		return nil
	}

	var logBuf bytes.Buffer
	oldLog := log.Writer()
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(oldLog) })

	t.Setenv("ENGRAM_CLOUD_AUTOSYNC", "1")
	t.Setenv(mcp.EnvHTTPToken, "")
	t.Setenv("ENGRAM_CLOUD_SERVER", "https://cloud.example.test")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "")

	withArgs(t, "engram", "mcp", "--transport=http")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdMCP(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("expected clean startup, got panic=%v stderr=%q", recovered, stderr)
	}
	if !served {
		t.Fatal("expected the HTTP transport to be served with CloudAuth wired")
	}
	out := logBuf.String()
	if strings.Contains(out, "ERROR: cloud token is not configured") {
		t.Fatalf("bearer-only boot must not log the token ERROR, got %q", out)
	}
	if !strings.Contains(out, "waiting for the first authenticated request") {
		t.Fatalf("expected the bearer-only info line, got %q", out)
	}
}

// TestTryStartAutosync_MissingTokenStaysErrorOutsideHTTPBearerOnly keeps
// REQ-211 for stdio / `engram serve`: the ERROR line is unchanged there.
func TestTryStartAutosync_MissingTokenStaysErrorOutsideHTTPBearerOnly(t *testing.T) {
	cfg := testConfig(t)
	s, err := store.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	var logBuf bytes.Buffer
	oldLog := log.Writer()
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(oldLog) })

	t.Setenv("ENGRAM_CLOUD_AUTOSYNC", "1")
	t.Setenv("ENGRAM_CLOUD_SERVER", "https://cloud.example.test")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "")

	tryStartAutosync(context.Background(), s, cfg)
	if !strings.Contains(logBuf.String(), "ERROR: cloud token is not configured") {
		t.Fatalf("expected the REQ-211 ERROR outside HTTP bearer-only mode, got %q", logBuf.String())
	}
}

// TestCmdMCPHTTP_CloudModeWiresWriteProjectEnrollment: writes whose project
// comes from a tool argument/session/default (not only the header) must be
// enrolled through the authenticator's cached, idempotent path.
func TestCmdMCPHTTP_CloudModeWiresWriteProjectEnrollment(t *testing.T) {
	cfg := testConfig(t)
	stubRuntimeHooks(t)
	stubExitWithPanic(t)
	gotCfg, _ := setupLazyCloudTestServer(t)

	t.Setenv("ENGRAM_CLOUD_AUTOSYNC", "1")
	t.Setenv(mcp.EnvHTTPToken, "")
	t.Setenv("ENGRAM_CLOUD_SERVER", "https://cloud.example.test")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "")

	withArgs(t, "engram", "mcp", "--transport=http")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdMCP(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("expected clean run, got panic=%v stderr=%q", recovered, stderr)
	}
	authImpl, ok := gotCfg.CloudAuth.(*cloudBearerAuthenticator)
	if !ok {
		t.Fatalf("expected CloudAuth to be a *cloudBearerAuthenticator, got %T", gotCfg.CloudAuth)
	}
	if gotCfg.OnWriteProject == nil {
		t.Fatal("expected HTTPTransportConfig.OnWriteProject to be wired in cloud mode")
	}
	var enrolled []string
	authImpl.enrollProject = func(p string) error { enrolled = append(enrolled, p); return nil }
	gotCfg.OnWriteProject("arg-proj")
	gotCfg.OnWriteProject("arg-proj")
	if len(enrolled) != 1 || enrolled[0] != "arg-proj" {
		t.Fatalf("enrolled = %v; want exactly one enrollment of arg-proj", enrolled)
	}
}

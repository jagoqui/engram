package main

import (
	"context"
	"strings"
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

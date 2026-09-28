package main

import (
	"context"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v2/internal/mcp"
	"github.com/Gentleman-Programming/engram/v2/internal/store"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

func TestCmdMCP_TransportFlag(t *testing.T) {
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
	oldServeMCP := serveMCP
	t.Cleanup(func() { serveMCP = oldServeMCP })

	t.Run("default transport is stdio", func(t *testing.T) {
		stdioCalled, httpCalled := false, false
		serveMCP = func(_ *mcpserver.MCPServer, _ ...mcpserver.StdioOption) error {
			stdioCalled = true
			return nil
		}
		serveMCPHTTP = func(_ context.Context, _ *mcpserver.MCPServer, _ mcp.HTTPTransportConfig) error {
			httpCalled = true
			return nil
		}

		withArgs(t, "engram", "mcp")
		_, stderr, recovered := captureOutputAndRecover(t, func() { cmdMCP(cfg) })
		if recovered != nil || stderr != "" {
			t.Fatalf("expected clean run, got panic=%v stderr=%q", recovered, stderr)
		}
		if !stdioCalled || httpCalled {
			t.Fatalf("expected stdio transport by default, stdioCalled=%v httpCalled=%v", stdioCalled, httpCalled)
		}
	})

	t.Run("--transport=http dispatches to serveMCPHTTP", func(t *testing.T) {
		stdioCalled := false
		var gotCfg mcp.HTTPTransportConfig
		httpCalled := false
		serveMCP = func(_ *mcpserver.MCPServer, _ ...mcpserver.StdioOption) error {
			stdioCalled = true
			return nil
		}
		serveMCPHTTP = func(_ context.Context, _ *mcpserver.MCPServer, cfg mcp.HTTPTransportConfig) error {
			httpCalled = true
			gotCfg = cfg
			return nil
		}

		withArgs(t, "engram", "mcp", "--transport=http")
		_, stderr, recovered := captureOutputAndRecover(t, func() { cmdMCP(cfg) })
		if recovered != nil || stderr != "" {
			t.Fatalf("expected clean run, got panic=%v stderr=%q", recovered, stderr)
		}
		if stdioCalled || !httpCalled {
			t.Fatalf("expected HTTP transport, stdioCalled=%v httpCalled=%v", stdioCalled, httpCalled)
		}
		if gotCfg.ListenAddr != "" {
			t.Fatalf("ListenAddr = %q; want empty (no --listen given)", gotCfg.ListenAddr)
		}
	})

	t.Run("--transport http (separate arg) dispatches to serveMCPHTTP", func(t *testing.T) {
		httpCalled := false
		serveMCPHTTP = func(_ context.Context, _ *mcpserver.MCPServer, _ mcp.HTTPTransportConfig) error {
			httpCalled = true
			return nil
		}

		withArgs(t, "engram", "mcp", "--transport", "http")
		_, stderr, recovered := captureOutputAndRecover(t, func() { cmdMCP(cfg) })
		if recovered != nil || stderr != "" {
			t.Fatalf("expected clean run, got panic=%v stderr=%q", recovered, stderr)
		}
		if !httpCalled {
			t.Fatal("expected --transport http to dispatch to serveMCPHTTP")
		}
	})

	t.Run("--listen flag is forwarded as ListenAddr", func(t *testing.T) {
		var gotCfg mcp.HTTPTransportConfig
		serveMCPHTTP = func(_ context.Context, _ *mcpserver.MCPServer, cfg mcp.HTTPTransportConfig) error {
			gotCfg = cfg
			return nil
		}

		withArgs(t, "engram", "mcp", "--transport=http", "--listen=0.0.0.0:9000")
		_, stderr, recovered := captureOutputAndRecover(t, func() { cmdMCP(cfg) })
		if recovered != nil || stderr != "" {
			t.Fatalf("expected clean run, got panic=%v stderr=%q", recovered, stderr)
		}
		if gotCfg.ListenAddr != "0.0.0.0:9000" {
			t.Fatalf("ListenAddr = %q; want %q", gotCfg.ListenAddr, "0.0.0.0:9000")
		}
	})

	t.Run("--listen as separate arg is forwarded", func(t *testing.T) {
		var gotCfg mcp.HTTPTransportConfig
		serveMCPHTTP = func(_ context.Context, _ *mcpserver.MCPServer, cfg mcp.HTTPTransportConfig) error {
			gotCfg = cfg
			return nil
		}

		withArgs(t, "engram", "mcp", "--transport", "http", "--listen", "127.0.0.1:9001")
		_, stderr, recovered := captureOutputAndRecover(t, func() { cmdMCP(cfg) })
		if recovered != nil || stderr != "" {
			t.Fatalf("expected clean run, got panic=%v stderr=%q", recovered, stderr)
		}
		if gotCfg.ListenAddr != "127.0.0.1:9001" {
			t.Fatalf("ListenAddr = %q; want %q", gotCfg.ListenAddr, "127.0.0.1:9001")
		}
	})

	t.Run("ENGRAM_MCP_HTTP_TOKEN is forwarded as LocalToken", func(t *testing.T) {
		t.Setenv(mcp.EnvHTTPToken, "  secret-token  ")
		var gotCfg mcp.HTTPTransportConfig
		serveMCPHTTP = func(_ context.Context, _ *mcpserver.MCPServer, cfg mcp.HTTPTransportConfig) error {
			gotCfg = cfg
			return nil
		}

		withArgs(t, "engram", "mcp", "--transport=http")
		_, stderr, recovered := captureOutputAndRecover(t, func() { cmdMCP(cfg) })
		if recovered != nil || stderr != "" {
			t.Fatalf("expected clean run, got panic=%v stderr=%q", recovered, stderr)
		}
		if gotCfg.LocalToken != "secret-token" {
			t.Fatalf("LocalToken = %q; want %q", gotCfg.LocalToken, "secret-token")
		}
	})

	t.Run("unknown --transport value fails clearly", func(t *testing.T) {
		withArgs(t, "engram", "mcp", "--transport=carrier-pigeon")
		_, stderr, recovered := captureOutputAndRecover(t, func() { cmdMCP(cfg) })
		code, ok := recovered.(exitCode)
		if !ok || int(code) != 1 {
			t.Fatalf("expected exit code 1 panic, got %v", recovered)
		}
		if !strings.Contains(stderr, "transport") {
			t.Fatalf("expected stderr to mention transport, got %q", stderr)
		}
	})

	t.Run("--transport with no value fails clearly", func(t *testing.T) {
		withArgs(t, "engram", "mcp", "--transport")
		_, stderr, recovered := captureOutputAndRecover(t, func() { cmdMCP(cfg) })
		code, ok := recovered.(exitCode)
		if !ok || int(code) != 1 {
			t.Fatalf("expected exit code 1 panic, got %v", recovered)
		}
		if !strings.Contains(stderr, "--transport requires a value") {
			t.Fatalf("expected stderr to mention missing value, got %q", stderr)
		}
	})

	t.Run("--listen with no value fails clearly", func(t *testing.T) {
		withArgs(t, "engram", "mcp", "--transport=http", "--listen")
		_, stderr, recovered := captureOutputAndRecover(t, func() { cmdMCP(cfg) })
		code, ok := recovered.(exitCode)
		if !ok || int(code) != 1 {
			t.Fatalf("expected exit code 1 panic, got %v", recovered)
		}
		if !strings.Contains(stderr, "--listen requires a value") {
			t.Fatalf("expected stderr to mention missing value, got %q", stderr)
		}
	})
}

// TestRunMCPHTTPSIGTERMRunsGracefulShutdown mirrors
// TestCmdMCPStdioSIGTERMRunsGracefulShutdown for the HTTP transport: a
// SIGTERM must cancel the context mcpServeHTTP runs under, so ServeHTTP's
// http.Server.Shutdown path fires instead of the process hanging.
func TestRunMCPHTTPSIGTERMRunsGracefulShutdown(t *testing.T) {
	oldNotify, oldStop := notifySignals, stopSignals
	registered := make(chan chan<- os.Signal, 1)
	notifySignals = func(ch chan<- os.Signal, _ ...os.Signal) { registered <- ch }
	stopSignals = func(chan<- os.Signal) {}
	t.Cleanup(func() {
		notifySignals = oldNotify
		stopSignals = oldStop
	})

	oldServeHTTP := mcpServeHTTP
	t.Cleanup(func() { mcpServeHTTP = oldServeHTTP })
	mcpServeHTTP = func(ctx context.Context, _ *mcpserver.MCPServer, _ mcp.HTTPTransportConfig) error {
		<-ctx.Done()
		return ctx.Err()
	}

	watchdog := time.AfterFunc(10*time.Second, func() {
		panic("runMCPHTTP did not return after SIGTERM")
	})
	defer watchdog.Stop()

	go func() {
		var sigCh chan<- os.Signal
		select {
		case sigCh = <-registered:
		case <-time.After(5 * time.Second):
			panic("runMCPHTTP did not register signal handling")
		}
		sigCh <- syscall.SIGTERM
	}()

	err := runMCPHTTP(context.Background(), mcpserver.NewMCPServer("test", "0"), mcp.HTTPTransportConfig{})
	if err != nil && err != context.Canceled {
		t.Fatalf("expected clean shutdown or context.Canceled, got %v", err)
	}
}

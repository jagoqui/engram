package mcp

import (
	"context"
	"crypto/subtle"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v2/internal/store"
	"github.com/mark3labs/mcp-go/server"
)

const (
	// DefaultHTTPListenAddr is the bind address `engram mcp --transport=http`
	// uses when neither --listen nor ENGRAM_MCP_HTTP_ADDR is set.
	DefaultHTTPListenAddr = "127.0.0.1:7438"

	// EnvHTTPListenAddr overrides DefaultHTTPListenAddr. The --listen flag
	// wins over this when both are set (see ResolveHTTPListenAddr).
	EnvHTTPListenAddr = "ENGRAM_MCP_HTTP_ADDR"

	// EnvHTTPToken, when set, guards the HTTP transport: a request whose
	// bearer token does not match it (constant-time compare) is rejected
	// with 401 before it reaches MCP. When unset, this local guard is a
	// no-op — a later cloud integration validates bearer tokens against
	// Engram Cloud instead.
	EnvHTTPToken = "ENGRAM_MCP_HTTP_TOKEN"

	httpMCPEndpointPath = "/mcp"
	httpHealthPath      = "/health"

	headerSubproject    = "X-Engram-Subproject"
	headerProjectAlias  = "X-Engram-Project"
	headerAuthorization = "Authorization"
	bearerPrefix        = "Bearer "
)

// HTTPTransportConfig configures the streamable HTTP MCP transport.
type HTTPTransportConfig struct {
	// ListenAddr is the address to bind, e.g. "127.0.0.1:7438". Empty
	// resolves via ResolveHTTPListenAddr (ENGRAM_MCP_HTTP_ADDR, then
	// DefaultHTTPListenAddr).
	ListenAddr string

	// LocalToken, when non-empty, guards every /mcp request: its bearer
	// token must match via constant-time comparison or the request is
	// rejected with 401. Empty disables the local guard.
	LocalToken string
}

// ResolveHTTPListenAddr applies the documented precedence for the HTTP
// transport's bind address: an explicit non-blank --listen flag value wins,
// then ENGRAM_MCP_HTTP_ADDR, then DefaultHTTPListenAddr.
func ResolveHTTPListenAddr(flagValue string) string {
	if v := strings.TrimSpace(flagValue); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv(EnvHTTPListenAddr)); v != "" {
		return v
	}
	return DefaultHTTPListenAddr
}

// NewHTTPHandler builds the http.Handler for the streamable HTTP MCP
// transport: POST/GET/DELETE /mcp for MCP itself (mark3labs/mcp-go's
// StreamableHTTPServer), and GET /health for a liveness probe, both behind
// the optional local bearer-token guard.
func NewHTTPHandler(mcpSrv *server.MCPServer, cfg HTTPTransportConfig) http.Handler {
	streamable := server.NewStreamableHTTPServer(mcpSrv,
		server.WithEndpointPath(httpMCPEndpointPath),
		server.WithHTTPContextFunc(httpRequestContextFunc),
	)

	mux := http.NewServeMux()
	mux.HandleFunc(httpHealthPath, handleHealth)
	mux.Handle(httpMCPEndpointPath, streamable)

	return withLocalBearerGuard(mux, cfg.LocalToken)
}

// ServeHTTP starts the streamable HTTP MCP transport and blocks until ctx is
// canceled or the underlying server errors. It logs a startup warning when
// binding to a non-loopback address with no local guard (ENGRAM_MCP_HTTP_TOKEN)
// configured, since that combination accepts unauthenticated requests from
// any reachable client.
func ServeHTTP(ctx context.Context, mcpSrv *server.MCPServer, cfg HTTPTransportConfig) error {
	addr := ResolveHTTPListenAddr(cfg.ListenAddr)
	if strings.TrimSpace(cfg.LocalToken) == "" && !isLoopbackAddr(addr) {
		log.Printf("[mcp-http] WARNING: listening on %s with no %s configured — this endpoint accepts unauthenticated requests from any reachable client", addr, EnvHTTPToken)
	}

	httpSrv := &http.Server{
		Addr:    addr,
		Handler: NewHTTPHandler(mcpSrv, cfg),
	}

	errCh := make(chan error, 1)
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return <-errCh
	case err := <-errCh:
		return err
	}
}

func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	host = strings.TrimSpace(host)
	if host == "" || strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// httpRequestContextFunc injects the per-request project override (from the
// X-Engram-Subproject header, or its X-Engram-Project alias) and the raw
// bearer token into ctx, and marks ctx as HTTP-transport-originated so
// project resolution never falls back to the container's own cwd.
func httpRequestContextFunc(ctx context.Context, r *http.Request) context.Context {
	ctx = withHTTPTransport(ctx)

	if project := requestProjectHeader(r); project != "" {
		ctx = WithRequestProject(ctx, project)
	}
	if token := bearerToken(r); token != "" {
		ctx = WithRequestBearerToken(ctx, token)
	}
	return ctx
}

// requestProjectHeader reads the per-request project override header.
// X-Engram-Subproject wins when both it and its X-Engram-Project alias are
// present. The value is normalized the same way project names are
// normalized elsewhere in Engram (store.NormalizeProject), so a header like
// " My App " resolves to the same bucket as an explicit "my app" argument.
func requestProjectHeader(r *http.Request) string {
	raw := strings.TrimSpace(r.Header.Get(headerSubproject))
	if raw == "" {
		raw = strings.TrimSpace(r.Header.Get(headerProjectAlias))
	}
	if raw == "" {
		return ""
	}
	normalized, _ := store.NormalizeProject(raw)
	return normalized
}

// bearerToken extracts the raw token from a "Bearer <token>" Authorization
// header. It returns "" for an absent header or any other auth scheme.
func bearerToken(r *http.Request) string {
	raw := strings.TrimSpace(r.Header.Get(headerAuthorization))
	if raw == "" || !strings.HasPrefix(raw, bearerPrefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(raw, bearerPrefix))
}

// withLocalBearerGuard rejects requests with 401 when localToken is set and
// the request's bearer token does not match it (constant-time compare). When
// localToken is empty, the guard is a no-op — a later cloud integration
// validates bearer tokens against Engram Cloud instead. /health is exempt so
// liveness probes never need a token.
func withLocalBearerGuard(next http.Handler, localToken string) http.Handler {
	trimmed := strings.TrimSpace(localToken)
	if trimmed == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == httpHealthPath {
			next.ServeHTTP(w, r)
			return
		}
		got := bearerToken(r)
		if subtle.ConstantTimeCompare([]byte(got), []byte(trimmed)) != 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

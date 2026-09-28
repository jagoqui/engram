package mcp

import (
	"context"
	"crypto/subtle"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"slices"
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

	// EnvHTTPAllowedOrigins is a comma-separated exact-match Origin allowlist; any Origin header is rejected unless listed here.
	EnvHTTPAllowedOrigins = "ENGRAM_MCP_HTTP_ALLOWED_ORIGINS"

	// EnvHTTPAllowedHosts is a comma-separated Host allowlist beyond loopback, checked only when EnvHTTPToken is unset (DNS-rebinding guard).
	EnvHTTPAllowedHosts = "ENGRAM_MCP_HTTP_ALLOWED_HOSTS"

	httpMCPEndpointPath = "/mcp"
	httpHealthPath      = "/health"

	// httpReadHeaderTimeout/httpIdleTimeout bound how long the HTTP server
	// waits on a slow client's headers or an idle keep-alive connection
	// (R4-no-server-timeouts).
	httpReadHeaderTimeout = 10 * time.Second
	httpIdleTimeout       = 120 * time.Second

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

	// AllowedOrigins is the raw comma-separated ENGRAM_MCP_HTTP_ALLOWED_ORIGINS value.
	AllowedOrigins string

	// AllowedHosts is the raw comma-separated ENGRAM_MCP_HTTP_ALLOWED_HOSTS value, used only when LocalToken is empty.
	AllowedHosts string

	// CloudAuth, when non-nil, replaces the static LocalToken guard for the
	// /mcp endpoint with per-request Engram Cloud bearer validation (T2).
	// The caller (cmd/engram) is responsible for enforcing that LocalToken
	// and CloudAuth are never both configured; this package deterministically
	// prefers CloudAuth when both are set.
	CloudAuth CloudBearerAuthenticator
}

// CloudBearerAuthenticator lets the streamable HTTP transport delegate all
// bearer-token handling for cloud mode to an Engram Cloud-aware caller
// (cmd/engram), so this package never imports internal/cloud/* directly —
// background/cloud orchestration stays out of the transport layer per
// engram-architecture-guardrails. HTTPTransportConfig.CloudAuth carries the
// implementation.
type CloudBearerAuthenticator interface {
	// Authenticate validates the request's raw bearer token (empty when the
	// request sent none) for project (empty when the request sent no
	// project header), and reports the outcome:
	//   - (true, nil): allow the request through.
	//   - (false, nil): reject with 401 (missing or invalid credentials).
	//   - (false, err): reject with 503 (cloud unreachable) — err is safe to
	//     log; implementations must never let it echo the raw token.
	Authenticate(ctx context.Context, token, project string) (bool, error)
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

	var guarded http.Handler = withLocalBearerGuard(mux, cfg.LocalToken)
	if cfg.CloudAuth != nil {
		guarded = withCloudBearerGuard(mux, cfg.CloudAuth)
	}
	return withOriginHostGuard(guarded, cfg)
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
		// R4-no-server-timeouts: bound slow/stalled clients instead of
		// leaving connections open indefinitely.
		ReadHeaderTimeout: httpReadHeaderTimeout,
		IdleTimeout:       httpIdleTimeout,
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

// withCloudBearerGuard authenticates every /mcp request against Engram Cloud
// through auth (bearer validation + fallback token + sync-token override +
// project enrollment — see cmd/engram's cloudBearerAuthenticator), rejecting
// with 401 (invalid/missing credentials) or 503 (cloud unreachable) before
// MCP dispatch. /health stays open, matching withLocalBearerGuard. It builds
// ctx via httpRequestContextFunc — the same per-request project/bearer
// extraction the streamable transport itself uses — so auth sees exactly
// what the eventual tool call will see.
func withCloudBearerGuard(next http.Handler, auth CloudBearerAuthenticator) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == httpHealthPath {
			next.ServeHTTP(w, r)
			return
		}

		ctx := httpRequestContextFunc(r.Context(), r)
		token, _ := RequestBearerToken(ctx)
		project, _ := requestProjectFromContext(ctx)

		ok, err := auth.Authenticate(ctx, token, project)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"cloud sync is unavailable"}`))
			return
		}
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// withOriginHostGuard rejects DNS-rebinding/cross-origin requests with 403 before MCP dispatch; /health stays open.
func withOriginHostGuard(next http.Handler, cfg HTTPTransportConfig) http.Handler {
	allowedOrigins := parseCommaList(cfg.AllowedOrigins)
	allowedHosts := parseCommaList(cfg.AllowedHosts)
	// A configured LocalToken or a CloudAuth already requires a bearer
	// secret to get through, which defeats rebinding just as well as
	// LocalToken alone — so the Host allowlist check below is skipped for
	// either.
	hasToken := strings.TrimSpace(cfg.LocalToken) != "" || cfg.CloudAuth != nil

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == httpHealthPath {
			next.ServeHTTP(w, r)
			return
		}
		if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" && !slices.Contains(allowedOrigins, origin) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		// A configured token already defeats rebinding, so skip the Host check.
		if !hasToken && !hostAllowed(r.Host, allowedHosts) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// hostAllowed reports whether hostHeader (with an optional port) is loopback or in allowed.
func hostAllowed(hostHeader string, allowed []string) bool {
	host := hostHeader
	if h, _, err := net.SplitHostPort(hostHeader); err == nil {
		host = h
	}
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return true
	}
	return slices.ContainsFunc(allowed, func(a string) bool { return strings.EqualFold(a, host) })
}

// parseCommaList splits a comma-separated env value into trimmed, non-empty entries.
func parseCommaList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

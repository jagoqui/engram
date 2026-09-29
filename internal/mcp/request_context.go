package mcp

import (
	"context"
	"errors"
	"strings"

	projectpkg "github.com/Gentleman-Programming/engram/v2/internal/project"
)

// requestContextKey namespaces context values this package attaches to a
// single MCP tool call, so they never collide with keys other packages set
// on the same context.
type requestContextKey int

const (
	ctxKeyRequestProject requestContextKey = iota
	ctxKeyHTTPTransport
	ctxKeyBearerToken
	ctxKeyWriteProjectHook
)

// ErrHTTPProjectRequired is returned when an MCP tool call arrives over the
// streamable HTTP transport and no project could be resolved from the
// per-call "project" tool argument, the request-scoped project (the
// X-Engram-Subproject header, or its X-Engram-Project alias), or the
// server's configured default project (MCPConfig.DefaultProject /
// ENGRAM_PROJECT). The HTTP transport's own working directory belongs to
// the container the server runs in, not the remote client, so project
// resolution never falls back to it.
var ErrHTTPProjectRequired = errors.New("no project resolved for this request: send the X-Engram-Subproject header (or its X-Engram-Project alias) naming the project, pass a project tool argument, or configure a server default project")

// WithRequestProject attaches a per-request project override to ctx. The
// streamable HTTP transport calls this (via server.WithHTTPContextFunc) with
// the normalized value of the X-Engram-Subproject request header (or its
// X-Engram-Project alias), so tool handlers resolve the effective project
// with the precedence: explicit per-call "project" tool argument > this
// context value > MCPConfig.DefaultProject. The stdio transport never sets
// this, so stdio resolution is unaffected.
func WithRequestProject(ctx context.Context, project string) context.Context {
	return context.WithValue(ctx, ctxKeyRequestProject, project)
}

// requestProjectFromContext returns the per-request project attached by
// WithRequestProject, if any non-blank value is present.
func requestProjectFromContext(ctx context.Context) (string, bool) {
	v, _ := ctx.Value(ctxKeyRequestProject).(string)
	v = strings.TrimSpace(v)
	if v == "" {
		return "", false
	}
	return v, true
}

// withHTTPTransport marks ctx as originating from the streamable HTTP
// transport. Project resolution consults this to refuse falling back to the
// server process's own working directory, which has no relationship to a
// remote client and must never leak into remote project resolution.
func withHTTPTransport(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxKeyHTTPTransport, true)
}

// isHTTPTransport reports whether ctx originated from the streamable HTTP
// transport.
func isHTTPTransport(ctx context.Context) bool {
	v, _ := ctx.Value(ctxKeyHTTPTransport).(bool)
	return v
}

// WithRequestBearerToken attaches the raw bearer token extracted from an
// HTTP request's Authorization header to ctx. It is exported so a future
// cloud-sync integration can read the per-request token; T1 only extracts
// and carries it.
func WithRequestBearerToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, ctxKeyBearerToken, token)
}

// RequestBearerToken returns the bearer token attached to ctx by the HTTP
// transport, if any.
func RequestBearerToken(ctx context.Context) (string, bool) {
	v, _ := ctx.Value(ctxKeyBearerToken).(string)
	v = strings.TrimSpace(v)
	if v == "" {
		return "", false
	}
	return v, true
}

// withWriteProjectHook attaches the HTTP transport's OnWriteProject callback
// to ctx. Nil hooks are not stored.
func withWriteProjectHook(ctx context.Context, hook func(project string)) context.Context {
	if hook == nil {
		return ctx
	}
	return context.WithValue(ctx, ctxKeyWriteProjectHook, hook)
}

// notifyWriteProject reports a successfully resolved write project to the
// HTTP transport's hook (used for cloud sync enrollment) and passes the
// resolution result through unchanged. It is a no-op outside HTTP mode.
func notifyWriteProject(ctx context.Context, res projectpkg.DetectionResult, err error) (projectpkg.DetectionResult, error) {
	if err != nil || !isHTTPTransport(ctx) {
		return res, err
	}
	if hook, _ := ctx.Value(ctxKeyWriteProjectHook).(func(string)); hook != nil {
		if p := strings.TrimSpace(res.Project); p != "" {
			hook(p)
		}
	}
	return res, err
}

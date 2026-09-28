package mcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// ─── Context helper round-trips ───────────────────────────────────────────

func TestWithRequestProjectRoundTrip(t *testing.T) {
	ctx := WithRequestProject(context.Background(), "my-project")
	got, ok := requestProjectFromContext(ctx)
	if !ok || got != "my-project" {
		t.Fatalf("requestProjectFromContext = (%q, %v); want (my-project, true)", got, ok)
	}
}

func TestRequestProjectFromContextAbsentOrBlank(t *testing.T) {
	if _, ok := requestProjectFromContext(context.Background()); ok {
		t.Fatal("expected no request project on a bare context")
	}
	ctx := WithRequestProject(context.Background(), "   ")
	if _, ok := requestProjectFromContext(ctx); ok {
		t.Fatal("expected a blank request project to be treated as absent")
	}
}

func TestHTTPTransportMarkerRoundTrip(t *testing.T) {
	if isHTTPTransport(context.Background()) {
		t.Fatal("a bare context must not read as HTTP transport")
	}
	ctx := withHTTPTransport(context.Background())
	if !isHTTPTransport(ctx) {
		t.Fatal("expected withHTTPTransport to mark the context")
	}
}

func TestWithRequestBearerTokenRoundTrip(t *testing.T) {
	ctx := WithRequestBearerToken(context.Background(), "secret-token")
	got, ok := RequestBearerToken(ctx)
	if !ok || got != "secret-token" {
		t.Fatalf("RequestBearerToken = (%q, %v); want (secret-token, true)", got, ok)
	}
}

func TestRequestBearerTokenAbsentOrBlank(t *testing.T) {
	if _, ok := RequestBearerToken(context.Background()); ok {
		t.Fatal("expected no bearer token on a bare context")
	}
	ctx := WithRequestBearerToken(context.Background(), "  ")
	if _, ok := RequestBearerToken(ctx); ok {
		t.Fatal("expected a blank bearer token to be treated as absent")
	}
}

// ─── Project resolution precedence: explicit > header > default > cwd ────

// chdirAmbiguousParent puts cwd inside a directory containing two sibling git
// repos, so projectpkg.DetectProjectFull(cwd) returns ErrAmbiguousProject —
// this proves a passing resolution came from the process override/header
// tier, never from an accidental cwd match.
func chdirAmbiguousParent(t *testing.T) {
	t.Helper()
	parent := t.TempDir()
	for _, name := range []string{"repo-a", "repo-b"} {
		child := filepath.Join(parent, name)
		if err := os.MkdirAll(child, 0o755); err != nil {
			t.Fatal(err)
		}
		initTestGitRepo(t, child)
	}
	t.Chdir(parent)
}

func TestResolveReadProjectWithProcessOverride_HeaderBeatsDefault(t *testing.T) {
	chdirAmbiguousParent(t)

	s := newMCPTestStore(t)
	if err := s.CreateSession("header-sess", "header project", t.TempDir()); err != nil {
		t.Fatalf("seed header project: %v", err)
	}

	ctx := WithRequestProject(context.Background(), "Header Project")
	res, err := resolveReadProjectWithProcessOverride(ctx, s, "", "default project")
	if err != nil {
		t.Fatalf("resolveReadProjectWithProcessOverride: %v", err)
	}
	if res.Project != "header project" {
		t.Fatalf("Project = %q; want %q (header must beat MCPConfig.DefaultProject)", res.Project, "header project")
	}
}

func TestResolveReadProjectWithProcessOverride_ExplicitBeatsHeaderAndDefault(t *testing.T) {
	chdirAmbiguousParent(t)

	s := newMCPTestStore(t)
	if err := s.CreateSession("explicit-sess", "explicit project", t.TempDir()); err != nil {
		t.Fatalf("seed explicit project: %v", err)
	}

	ctx := WithRequestProject(context.Background(), "header project")
	res, err := resolveReadProjectWithProcessOverride(ctx, s, "Explicit Project", "default project")
	if err != nil {
		t.Fatalf("resolveReadProjectWithProcessOverride: %v", err)
	}
	if res.Project != "explicit project" {
		t.Fatalf("Project = %q; want %q (per-call explicit argument must win)", res.Project, "explicit project")
	}
}

func TestResolveReadProjectWithProcessOverride_HeaderAloneResolves(t *testing.T) {
	chdirAmbiguousParent(t)

	s := newMCPTestStore(t)
	if err := s.CreateSession("solo-header-sess", "solo header project", t.TempDir()); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	ctx := WithRequestProject(context.Background(), "solo header project")
	res, err := resolveReadProjectWithProcessOverride(ctx, s, "", "")
	if err != nil {
		t.Fatalf("resolveReadProjectWithProcessOverride: %v", err)
	}
	if res.Project != "solo header project" {
		t.Fatalf("Project = %q; want %q", res.Project, "solo header project")
	}
}

// ─── HTTP mode never falls back to cwd detection ──────────────────────────

func TestResolveReadProjectWithProcessOverride_HTTPModeNoCwdFallback(t *testing.T) {
	// A real git repo in cwd — over stdio this alone would resolve a project.
	dir := t.TempDir()
	initTestGitRepo(t, dir)
	t.Chdir(dir)

	s := newMCPTestStore(t)
	ctx := withHTTPTransport(context.Background())
	_, err := resolveReadProjectWithProcessOverride(ctx, s, "", "")
	if !errors.Is(err, ErrHTTPProjectRequired) {
		t.Fatalf("error = %v; want ErrHTTPProjectRequired (HTTP mode must never fall back to server cwd)", err)
	}
}

func TestResolveReadProjectWithProcessOverride_HTTPModeHeaderStillResolves(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir) // no git repo here at all — cwd detection would basename-fallback over stdio

	s := newMCPTestStore(t)
	if err := s.CreateSession("http-header-sess", "http header project", t.TempDir()); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	ctx := withHTTPTransport(WithRequestProject(context.Background(), "http header project"))
	res, err := resolveReadProjectWithProcessOverride(ctx, s, "", "")
	if err != nil {
		t.Fatalf("resolveReadProjectWithProcessOverride: %v", err)
	}
	if res.Project != "http header project" {
		t.Fatalf("Project = %q; want %q", res.Project, "http header project")
	}
}

func TestResolveWriteProject_HTTPModeSkipsCwdDetection(t *testing.T) {
	dir := t.TempDir()
	initTestGitRepo(t, dir)
	t.Chdir(dir)

	ctx := withHTTPTransport(context.Background())
	res, err := resolveWriteProject(ctx)
	if err != nil {
		t.Fatalf("resolveWriteProject: %v", err)
	}
	if res.Project != "" || res.Source != "" {
		t.Fatalf("resolveWriteProject in HTTP mode = %+v; want a zero-value result (no cwd signal)", res)
	}
}

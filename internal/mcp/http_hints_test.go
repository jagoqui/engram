package mcp

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v2/internal/project"
)

func captureStdLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(old) })
	return &buf
}

func TestWarnIfUnauthenticatedListener(t *testing.T) {
	tests := []struct {
		name     string
		addr     string
		cfg      HTTPTransportConfig
		wantWarn bool
	}{
		{"non-loopback, no auth", "0.0.0.0:7438", HTTPTransportConfig{}, true},
		{"non-loopback, local token", "0.0.0.0:7438", HTTPTransportConfig{LocalToken: "s"}, false},
		{"non-loopback, cloud auth", "0.0.0.0:7438", HTTPTransportConfig{CloudAuth: &fakeCloudAuth{}}, false},
		{"loopback, no auth", "127.0.0.1:7438", HTTPTransportConfig{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := captureStdLog(t)
			warnIfUnauthenticatedListener(tt.addr, tt.cfg)
			got := strings.Contains(buf.String(), "WARNING")
			if got != tt.wantWarn {
				t.Fatalf("warning emitted = %v, want %v (log=%q)", got, tt.wantWarn, buf.String())
			}
		})
	}
}

func TestWriteProjectErrorResult_HTTPHintPointsToSubprojectHeader(t *testing.T) {
	res := project.DetectionResult{AvailableProjects: []string{"a", "b"}}

	httpBody := callResultJSON(t, writeProjectErrorResult(withHTTPTransport(context.Background()), nil, "s", res, project.ErrAmbiguousProject))
	hint, _ := httpBody["hint"].(string)
	if strings.Contains(hint, "cd into") || strings.Contains(hint, ".engram/config.json") {
		t.Fatalf("HTTP hint must not mention cwd/config guidance: %q", hint)
	}
	if !strings.Contains(hint, "X-Engram-Subproject") {
		t.Fatalf("HTTP hint must point to X-Engram-Subproject: %q", hint)
	}

	stdioBody := callResultJSON(t, writeProjectErrorResult(context.Background(), nil, "s", res, project.ErrAmbiguousProject))
	if h, _ := stdioBody["hint"].(string); !strings.Contains(h, "cd into the target repo or add repo .engram/config.json") {
		t.Fatalf("stdio hint must stay unchanged: %q", h)
	}
}

package mcp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	mcppkg "github.com/mark3labs/mcp-go/mcp"
)

// httpToolCaller returns a func that calls tool over a fresh streamable HTTP
// session carrying headers, against the server at handlerSrvURL.
func httpToolCaller(t *testing.T, handlerSrvURL string, headers map[string]string) func(tool string, args map[string]any) (string, bool) {
	t.Helper()
	c, err := mcpclient.NewStreamableHttpClient(handlerSrvURL+"/mcp", transport.WithHTTPHeaders(headers))
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	initReq := mcppkg.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcppkg.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcppkg.Implementation{Name: "t", Version: "0"}
	if _, err := c.Initialize(ctx, initReq); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	return func(tool string, args map[string]any) (string, bool) {
		req := mcppkg.CallToolRequest{}
		req.Params.Name = tool
		req.Params.Arguments = args
		res, err := c.CallTool(ctx, req)
		if err != nil {
			t.Fatalf("call %s: %v", tool, err)
		}
		text, _ := mcppkg.AsTextContent(res.Content[0])
		return text.Text, res.IsError
	}
}

func newHTTPProjectServer(t *testing.T, mcpCfg MCPConfig, httpCfg HTTPTransportConfig) (*httptest.Server, func(map[string]string) func(string, map[string]any) (string, bool)) {
	t.Helper()
	s := newMCPTestStore(t)
	srv := httptest.NewServer(NewHTTPHandler(NewServerWithConfig(s, mcpCfg, nil), httpCfg))
	t.Cleanup(srv.Close)
	return srv, func(h map[string]string) func(string, map[string]any) (string, bool) {
		return httpToolCaller(t, srv.URL, h)
	}
}

func TestHTTPTransport_OnWriteProjectReportsResolvedProject(t *testing.T) {
	var got []string
	_, caller := newHTTPProjectServer(t, MCPConfig{}, HTTPTransportConfig{OnWriteProject: func(p string) { got = append(got, p) }})

	save := func(call func(string, map[string]any) (string, bool), args map[string]any) {
		t.Helper()
		args["title"], args["content"] = "t", "c"
		if txt, isErr := call("mem_save", args); isErr {
			t.Fatalf("mem_save failed: %s", txt)
		}
	}
	// header only
	save(caller(map[string]string{"X-Engram-Subproject": "other"}), map[string]any{})
	// project tool argument (existing project) differing from the header
	save(caller(map[string]string{"X-Engram-Subproject": "hdr"}), map[string]any{"project": "other"})

	want := []string{"other", "other"}
	if len(got) < 2 || got[len(got)-1] != "other" || got[0] != "other" {
		t.Fatalf("OnWriteProject calls = %v; want the resolved write project each time (%v)", got, want)
	}
	for _, p := range got {
		if p == "hdr" {
			t.Fatalf("OnWriteProject reported the header project %q for a write resolved to the arg project: %v", p, got)
		}
	}
}

func TestHTTPTransport_OnWriteProjectReportsDefaultProject(t *testing.T) {
	var got []string
	_, caller := newHTTPProjectServer(t, MCPConfig{DefaultProject: "env-proj"}, HTTPTransportConfig{OnWriteProject: func(p string) { got = append(got, p) }})
	call := caller(nil)
	if txt, isErr := call("mem_save", map[string]any{"title": "t", "content": "c"}); isErr {
		t.Fatalf("mem_save failed: %s", txt)
	}
	if len(got) != 1 || got[0] != "env-proj" {
		t.Fatalf("OnWriteProject calls = %v; want [env-proj]", got)
	}
}

func TestHTTPTransport_CurrentProjectNeverUsesCwd(t *testing.T) {
	_, caller := newHTTPProjectServer(t, MCPConfig{}, HTTPTransportConfig{})

	txt, isErr := caller(map[string]string{"X-Engram-Subproject": "hdr-proj"})("mem_current_project", nil)
	if isErr {
		t.Fatalf("mem_current_project error: %s", txt)
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(txt), &env); err != nil {
		t.Fatalf("decode %q: %v", txt, err)
	}
	if env["project"] != "hdr-proj" {
		t.Fatalf("project = %v; want header project", env["project"])
	}
	if cwd, _ := env["cwd"].(string); cwd != "" {
		t.Fatalf("cwd = %q; the server cwd must not be exposed over HTTP", cwd)
	}

	txt, _ = caller(nil)("mem_current_project", nil)
	if err := json.Unmarshal([]byte(txt), &env); err != nil {
		t.Fatalf("decode %q: %v", txt, err)
	}
	if env["project"] != "" && env["project"] != nil {
		t.Fatalf("project = %v with no header/default; want empty (no cwd detection)", env["project"])
	}
	if hint, _ := env["error_hint"].(string); !strings.Contains(hint, "X-Engram-Subproject") {
		t.Fatalf("error_hint = %q; want it to require the X-Engram-Subproject header", hint)
	}
	if cwd, _ := env["cwd"].(string); cwd != "" {
		t.Fatalf("cwd = %q; must be blank over HTTP", cwd)
	}
}

func TestHTTPTransport_CurrentProjectUsesDefaultProject(t *testing.T) {
	_, caller := newHTTPProjectServer(t, MCPConfig{DefaultProject: "env-proj"}, HTTPTransportConfig{})
	txt, _ := caller(nil)("mem_current_project", nil)
	var env map[string]any
	if err := json.Unmarshal([]byte(txt), &env); err != nil || env["project"] != "env-proj" {
		t.Fatalf("got %q (%v); want project env-proj", txt, err)
	}
}

func TestHTTPTransport_ExplicitProjectMatchingHeaderCreatesNewProject(t *testing.T) {
	_, caller := newHTTPProjectServer(t, MCPConfig{}, HTTPTransportConfig{})
	call := caller(map[string]string{"X-Engram-Subproject": "brand-new"})

	if txt, isErr := call("mem_save", map[string]any{"title": "t", "content": "c", "project": "Brand-New"}); isErr {
		t.Fatalf("explicit project equal to the header project must succeed for a new project: %s", txt)
	}
	// A different explicit project that does not exist still fails as before.
	txt, isErr := call("mem_save", map[string]any{"title": "t2", "content": "c2", "project": "not-the-header"})
	if !isErr || !strings.Contains(txt, "unknown") {
		t.Fatalf("got (%q, isErr=%v); want unknown-project error for an explicit non-header project", txt, isErr)
	}
}

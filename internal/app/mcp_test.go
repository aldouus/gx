package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"gx/internal/cli"
	"gx/internal/config"
	"gx/internal/tool"
	"gx/internal/tui"
)

func rpcID(body []byte) string {
	var m struct {
		ID json.RawMessage `json:"id"`
	}
	if json.Unmarshal(body, &m) != nil || len(m.ID) == 0 {
		return "1"
	}
	return string(m.ID)
}

func TestMCPDialsOnFirstCall(t *testing.T) {
	var inits atomic.Int32
	var calls atomic.Int32
	var deleted atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleted.Store(true)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		body, _ := io.ReadAll(r.Body)
		switch {
		case bytes.Contains(body, []byte(`"initialize"`)):
			inits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Mcp-Session-Id", "sess-1")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18"}}`, rpcID(body))
		case bytes.Contains(body, []byte(`"notifications/initialized"`)):
			w.WriteHeader(http.StatusAccepted)
		case bytes.Contains(body, []byte(`"tools/list"`)):
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"get","description":"fetch","inputSchema":{"type":"object"}}]}}`, rpcID(body))
		case bytes.Contains(body, []byte(`"tools/call"`)):
			calls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"ok"}]}}`, rpcID(body))
		default:
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}))
	defer srv.Close()

	reg := tool.NewRegistry()
	pool := attachMCP(context.Background(), reg, []config.MCPServer{{Name: "docs", URL: srv.URL}}, io.Discard)
	if inits.Load() != 0 {
		t.Fatal("attach dialed before a call")
	}
	got, ok := reg.Get("mcp_docs")
	if !ok {
		t.Fatal("server tool was not registered")
	}
	out, err := got.Run(context.Background(), []byte(`{"tool":"get","arguments":{"q":"x"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "ok") {
		t.Fatalf("call %s", out)
	}
	if inits.Load() != 1 {
		t.Fatalf("inits %d", inits.Load())
	}
	if _, ok := reg.Get("mcp_docs_get"); !ok {
		t.Fatal("listed tool was not registered after the first call")
	}
	out, err = got.Run(context.Background(), []byte(`{"tool":"get"}`))
	if err != nil {
		t.Fatal(err)
	}
	if inits.Load() != 1 || calls.Load() != 2 {
		t.Fatalf("inits %d calls %d out %s", inits.Load(), calls.Load(), out)
	}
	closeMCP(pool)
	if !deleted.Load() {
		t.Fatal("session was not closed at the end of the turn")
	}
}

func TestMCPLoginApproveReject(t *testing.T) {
	home := t.TempDir()
	var buf bytes.Buffer
	tr := tui.NewTranscript(&buf, 80)
	settings := config.File{MCPServers: []config.MCPServer{{Name: "docs", URL: "https://example.test/mcp"}}}
	if err := config.Save(home, settings); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := handleSubmit(cli.Invocation{}, &loaded, home, t.TempDir(), tr, &buf, "/mcp login docs secret", nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := handleSubmit(cli.Invocation{}, &loaded, home, t.TempDir(), tr, &buf, "/mcp approve docs", nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	saved, err := config.Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if saved.MCPServers[0].Token != "secret" || !saved.MCPServers[0].Approved {
		t.Fatalf("%+v", saved.MCPServers[0])
	}
	if err := handleSubmit(cli.Invocation{}, &loaded, home, t.TempDir(), tr, &buf, "/mcp reject docs", nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	saved, err = config.Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if !saved.MCPServers[0].Rejected || saved.MCPServers[0].Approved {
		t.Fatalf("%+v", saved.MCPServers[0])
	}
	reg := tool.NewRegistry()
	attachMCP(context.Background(), reg, saved.MCPServers, io.Discard)
	if _, ok := reg.Get("mcp_docs"); ok {
		t.Fatal("rejected server was registered")
	}
}

func TestMCPSlashAddShowRemove(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GX_HOME", home)
	var buf bytes.Buffer
	tr := tui.NewTranscript(&buf, 80)
	settings := config.File{}
	if err := handleSubmit(cli.Invocation{}, &settings, home, t.TempDir(), tr, &buf, "/mcp add docs https://example.test/mcp", nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(settings.MCPServers) != 1 || settings.MCPServers[0].URL != "https://example.test/mcp" {
		t.Fatalf("%+v", settings.MCPServers)
	}
	loaded, err := config.Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.MCPServers) != 1 || loaded.MCPServers[0].Name != "docs" {
		t.Fatalf("saved %+v", loaded.MCPServers)
	}
	info, err := os.Stat(home + "/settings.json")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
	buf.Reset()
	if err := handleSubmit(cli.Invocation{}, &settings, home, t.TempDir(), tr, &buf, "/mcp show docs", nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "docs") || !strings.Contains(buf.String(), "https://example.test/mcp") {
		t.Fatal(buf.String())
	}
	buf.Reset()
	if err := handleSubmit(cli.Invocation{}, &settings, home, t.TempDir(), tr, &buf, "/mcp add local -- echo hi", nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(settings.MCPServers) != 2 || settings.MCPServers[1].Command != "echo" || settings.MCPServers[1].Args[0] != "hi" {
		t.Fatalf("%+v", settings.MCPServers)
	}
	buf.Reset()
	if err := handleSubmit(cli.Invocation{}, &settings, home, t.TempDir(), tr, &buf, "/mcp remove docs", nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	loaded, err = config.Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.MCPServers) != 1 || loaded.MCPServers[0].Name != "local" {
		t.Fatalf("after remove %+v", loaded.MCPServers)
	}
	if len(settings.MCPServers) != 1 {
		t.Fatal("in-memory settings were not updated")
	}
}

package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestProtocolVersion(t *testing.T) {
	if protocolVersion != "2025-11-25" {
		t.Fatalf("initialize offers %s", protocolVersion)
	}
	for _, v := range []string{"2025-11-25", "2025-06-18", "2025-03-26"} {
		if !acceptedVersion(v) {
			t.Fatalf("rejected %s", v)
		}
	}
	if acceptedVersion("2024-11-05") || acceptedVersion("2026-07-28") {
		t.Fatal("accepted a version initialize does not agree to")
	}
}

func TestSessionIDAndMediaType(t *testing.T) {
	if !validSessionID("1868a90c-9f") || validSessionID("") || validSessionID("has space") || validSessionID("cr\rlf") {
		t.Fatal("session id check")
	}
	long := strings.Repeat("a", 1025)
	if validSessionID(long) {
		t.Fatal("long session id")
	}
	if !isJSONType("application/json; charset=utf-8") || isJSONType("text/event-stream") {
		t.Fatal("json media type")
	}
	if !isEventStream("text/event-stream; charset=utf-8") || isEventStream("application/json") {
		t.Fatal("sse media type")
	}
}

func TestParsePageAndArgs(t *testing.T) {
	tools, next, err := parsePage([]byte(`{"tools":[{"name":"a","inputSchema":{}}],"nextCursor":null}`))
	if err != nil || next != nil || len(tools) != 1 {
		t.Fatalf("page: %v %v %v", tools, next, err)
	}
	_, next, err = parsePage([]byte(`{"tools":[],"nextCursor":""}`))
	if err != nil || next == nil || *next != "" {
		t.Fatalf("empty cursor: %v %v", next, err)
	}
	if _, _, err := parsePage([]byte(`{"nextCursor":"a"}`)); err == nil {
		t.Fatal("missing tools")
	}
	if _, _, err := parsePage([]byte(`{"tools":[],"nextCursor":7}`)); err == nil {
		t.Fatal("numeric cursor")
	}
	if _, err := compactObject(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := compactObject([]byte(`[1]`)); err == nil {
		t.Fatal("array arguments")
	}
	got, err := compactObject([]byte("{\n\"q\": \"x\"\n}"))
	if err != nil || string(got) != `{"q":"x"}` {
		t.Fatalf("compact %s %v", got, err)
	}
}

func TestIDMatch(t *testing.T) {
	if !idMatch([]byte("1"), 1) || !idMatch([]byte(`"1"`), 1) || idMatch([]byte("2"), 1) || idMatch([]byte("null"), 1) {
		t.Fatal("id match")
	}
}

func TestSignalGroupIgnoresInit(t *testing.T) {
	signalGroup(0, syscall.SIGTERM)
	signalGroup(1, syscall.SIGTERM)
}

func TestCommandEnvOverride(t *testing.T) {
	got := commandEnv([]string{"MCP_TEST_ONLY=1", "PATH=/tmp/bin"})
	found := map[string]string{}
	for _, kv := range got {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			t.Fatalf("bad env %q", kv)
		}
		if _, dup := found[k]; dup {
			t.Fatalf("duplicate %s", k)
		}
		found[k] = v
	}
	if found["MCP_TEST_ONLY"] != "1" || found["PATH"] != "/tmp/bin" {
		t.Fatalf("env %#v", found)
	}
	if commandEnv(nil) != nil {
		t.Fatal("empty extra should inherit")
	}
}

func TestDialRejectsBadTarget(t *testing.T) {
	ctx := context.Background()
	if _, err := Dial(ctx, Server{Name: "x"}); err == nil {
		t.Fatal("empty command")
	}
	if _, err := Dial(ctx, Server{Name: "x", URL: "ftp://example.com/mcp"}); err == nil {
		t.Fatal("ftp")
	}
	if _, err := Dial(ctx, Server{Name: "x", Command: "/nonexistent/mcp-server"}); err == nil {
		t.Fatal("missing binary")
	}
	start := time.Now()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err := Dial(cancelled, Server{Name: "x", Command: "/bin/sleep", Args: []string{"30"}})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("cancelled dial started a process")
	}
}

func TestDialStdioProcessExits(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	_, err := Dial(ctx, Server{Name: "gone", Command: "/bin/sh", Args: []string{"-c", "exit 0"}})
	if err == nil {
		t.Fatal("expected handshake error")
	}
	if time.Since(start) > time.Second {
		t.Fatal("close waited on a dead process")
	}
}

func TestStdioSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	clientR, clientW, serverR, serverW := pipes(t)

	var mu sync.Mutex
	var lines []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer serverW.Close()
		defer serverR.Close()
		br := bufio.NewReader(serverR)
		phase := 0
		var initID []byte
		for {
			line, err := br.ReadBytes('\n')
			if err != nil {
				return
			}
			line = bytes.TrimSpace(line)
			mu.Lock()
			lines = append(lines, string(line))
			mu.Unlock()
			msg, err := decode(line)
			if err != nil {
				return
			}
			switch phase {
			case 0:
				if msg.Method != "initialize" {
					return
				}
				initID = append([]byte(nil), bytes.TrimSpace(msg.ID)...)
				io.WriteString(serverW, `{"jsonrpc":"2.0","id":"ping-1","method":"ping"}`+"\n")
				phase = 1
			case 1:
				io.WriteString(serverW, `{"jsonrpc":"2.0","id":"srv","method":"roots/list"}`+"\n")
				phase = 2
			case 2:
				io.WriteString(serverW, `{"jsonrpc":"2.0","id":`+string(initID)+`,"result":{"protocolVersion":"2025-11-25","capabilities":{}}}`+"\n")
				phase = 3
			case 3:
				phase = 4
			default:
				replyStdio(serverW, line)
			}
		}
	}()

	s, err := connectPipe(ctx, "weather", clientR, clientW)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		s.Close()
		<-done
	}()

	tools, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 {
		t.Fatalf("tools %#v", tools)
	}
	if tools[0].Server != "weather" || tools[0].Name != "a<b" || tools[0].Description != "aye" {
		t.Fatalf("first %#v", tools[0])
	}
	if string(tools[0].Schema) != `{"type":"object"}` {
		t.Fatalf("schema %s", tools[0].Schema)
	}
	if tools[1].Name != "b" || tools[1].Description != "" || tools[1].Server != "weather" {
		t.Fatalf("second %#v", tools[1])
	}

	got, err := s.Call(ctx, "a<b", json.RawMessage("{\n\"q\": \"x\"\n}"))
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"content":[{"type":"text","text":"hi"}],"isError":false}`
	if got != want {
		t.Fatalf("call %s", got)
	}
	failed, err := s.Call(ctx, "missing", nil)
	if err == nil || failed != "" || !strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("rpc error %q %v", failed, err)
	}
	if _, err := s.Call(ctx, "a<b", json.RawMessage(`[1]`)); err == nil {
		t.Fatal("array args")
	}
	errored, err := s.Call(ctx, "a<b", json.RawMessage(`{"n":1}`))
	if err != nil || !strings.Contains(errored, `"isError":true`) {
		t.Fatalf("isError %s %v", errored, err)
	}

	var wg sync.WaitGroup
	errc := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := s.Call(ctx, "a<b", nil)
			if err != nil || !strings.Contains(out, `"text":"hi"`) {
				errc <- errors.New("concurrent call")
			}
		}()
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		t.Fatal(err)
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.List(ctx); !errors.Is(err, errClosed) {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(lines) < 4 {
		t.Fatalf("lines %v", lines)
	}
	var init rpcMessage
	if err := json.Unmarshal([]byte(lines[0]), &init); err != nil {
		t.Fatal(err)
	}
	if init.Method != "initialize" || !idMatch(init.ID, 1) {
		t.Fatalf("init %s", lines[0])
	}
	var params struct {
		Meta struct {
			ProgressToken int64 `json:"progressToken"`
		} `json:"_meta"`
		ProtocolVersion string `json:"protocolVersion"`
		Capabilities    json.RawMessage
		ClientInfo      clientInfo
	}
	if json.Unmarshal(mustParams(t, lines[0]), &params) != nil {
		t.Fatal(lines[0])
	}
	if params.ProtocolVersion != "2025-11-25" || params.Meta.ProgressToken != 1 || string(params.Capabilities) != "{}" {
		t.Fatalf("params %#v", params)
	}
	if params.ClientInfo.Name != "gx" {
		t.Fatal(params.ClientInfo)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, `"id":"ping-1","result":{}`) {
		t.Fatalf("ping reply missing\n%s", joined)
	}
	if !strings.Contains(joined, `"id":"srv"`) || !strings.Contains(joined, `"code":-32601`) {
		t.Fatalf("method not found missing\n%s", joined)
	}
	if !strings.Contains(joined, `"method":"notifications/initialized"`) || !strings.Contains(joined, `"params":{}`) {
		t.Fatalf("initialized missing\n%s", joined)
	}
	if !strings.Contains(joined, `"name":"a<b"`) || strings.Contains(joined, `\u003c`) {
		t.Fatalf("name escaped\n%s", joined)
	}
	if !strings.Contains(joined, `"cursor":"p2"`) {
		t.Fatalf("cursor missing\n%s", joined)
	}
	if !strings.Contains(joined, `"arguments":{}`) || !strings.Contains(joined, `"arguments":{"q":"x"}`) {
		t.Fatalf("arguments\n%s", joined)
	}
}

func TestStdioTimeoutAndBadVersion(t *testing.T) {
	clientR, clientW, serverR, serverW := pipes(t)
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer serverW.Close()
		br := bufio.NewReader(serverR)
		_, _ = br.ReadBytes('\n')
		<-release
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := connectPipe(ctx, "slow", clientR, clientW)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout: %v", err)
	}
	close(release)
	<-done

	clientR, clientW, serverR, serverW = pipes(t)
	done = make(chan struct{})
	go func() {
		defer close(done)
		defer serverW.Close()
		br := bufio.NewReader(serverR)
		line, err := br.ReadBytes('\n')
		if err != nil {
			return
		}
		msg, err := decode(line)
		if err != nil {
			return
		}
		io.WriteString(serverW, `{"jsonrpc":"2.0","id":`+string(bytes.TrimSpace(msg.ID))+`,"result":{"protocolVersion":"2024-11-05"}}`+"\n")
		io.Copy(io.Discard, br)
	}()
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = connectPipe(ctx, "old", clientR, clientW)
	<-done
	if err == nil || !strings.Contains(err.Error(), "2024-11-05") {
		t.Fatal(err)
	}
}

func TestHTTPSession(t *testing.T) {
	var (
		mu      sync.Mutex
		deleted atomic.Bool
		bodies  []string
		headers []http.Header
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			if r.Header.Get("Mcp-Session-Id") != "sess-1" || r.Header.Get("MCP-Protocol-Version") != "2025-06-18" {
				http.Error(w, "bad delete", http.StatusBadRequest)
				return
			}
			deleted.Store(true)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(body))
		headers = append(headers, r.Header.Clone())
		n := len(bodies)
		mu.Unlock()
		switch {
		case bytes.Contains(body, []byte(`"initialize"`)):
			if r.Header.Get("MCP-Protocol-Version") != "" || r.Header.Get("Mcp-Session-Id") != "" {
				http.Error(w, "initialize headers", http.StatusBadRequest)
				return
			}
			if r.Header.Get("Accept") != acceptPost || r.Header.Get("Content-Type") != "application/json" {
				http.Error(w, "accept", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Mcp-Session-Id", "sess-1")
			io.WriteString(w, ": keep-alive\n")
			io.WriteString(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/message\",\"params\":{}}\n\n")
			io.WriteString(w, "data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"protocolVersion\":\"2025-06-18\"}}\n\n")
		case bytes.Contains(body, []byte(`"notifications/initialized"`)):
			if r.Header.Get("Mcp-Session-Id") != "sess-1" || r.Header.Get("MCP-Protocol-Version") != "2025-06-18" {
				http.Error(w, "initialized headers", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusAccepted)
		case bytes.Contains(body, []byte(`"tools/list"`)):
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			io.WriteString(w, `{"jsonrpc":"2.0","id":`+idOf(body)+`,"result":{"tools":[{"name":"get","description":"fetch","inputSchema":{"type":"object"}},{"name":"","inputSchema":{}},{"name":"get","description":"later","inputSchema":{"type":"object"}}]}}`)
		case bytes.Contains(body, []byte(`"tools/call"`)):
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"jsonrpc":"2.0","id":`+idOf(body)+`,"result":{"content":[{"type":"text","text":"ok"}]}}`)
		default:
			http.Error(w, "unexpected "+string(body), http.StatusBadRequest)
		}
		_ = n
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s, err := Dial(ctx, Server{Name: "docs", URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name != "get" || tools[0].Server != "docs" || tools[0].Description != "fetch" {
		t.Fatalf("tools %#v", tools)
	}
	out, err := s.Call(ctx, "get", nil)
	if err != nil || !strings.Contains(out, `"text":"ok"`) {
		t.Fatalf("call %s %v", out, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if !deleted.Load() {
		t.Fatal("session was not deleted")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) < 2 || !strings.Contains(bodies[0], `"protocolVersion":"2025-11-25"`) {
		t.Fatalf("bodies %v", bodies)
	}
}

func TestHTTPNoSessionSkipsDelete(t *testing.T) {
	var deleted atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleted.Store(true)
			return
		}
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if bytes.Contains(body, []byte(`"initialize"`)) {
			io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26"}}`)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s, err := Dial(ctx, Server{Name: "n", URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if deleted.Load() {
		t.Fatal("deleted a session that had no id")
	}
}

func TestHTTPBadSessionID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Mcp-Session-Id", "bad id")
		io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-11-25"}}`)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := Dial(ctx, Server{Name: "n", URL: srv.URL})
	if err == nil || !strings.Contains(err.Error(), "session id") {
		t.Fatal(err)
	}
}

func pipes(t *testing.T) (clientR, clientW, serverR, serverW *os.File) {
	t.Helper()
	var err error
	serverR, clientW, err = os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	clientR, serverW, err = os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		clientR.Close()
		clientW.Close()
		serverR.Close()
		serverW.Close()
	})
	return clientR, clientW, serverR, serverW
}

func connectPipe(ctx context.Context, name string, r io.Reader, w io.WriteCloser) (*Session, error) {
	s := openPipe(name, r, w)
	if err := s.handshake(ctx); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

func replyStdio(w io.Writer, line []byte) {
	msg, err := decode(line)
	if err != nil {
		return
	}
	id := string(bytes.TrimSpace(msg.ID))
	raw := string(line)
	switch msg.Method {
	case "tools/list":
		if strings.Contains(raw, `"cursor"`) {
			io.WriteString(w, `{"jsonrpc":"2.0","id":`+id+`,"result":{"tools":[{"name":"b","inputSchema":{"type":"object"}},{"name":"a<b","description":"second","inputSchema":{"type":"object"}}]}}`+"\n")
			return
		}
		io.WriteString(w, `{"jsonrpc":"2.0","id":`+id+`,"result":{"tools":[{"name":"a<b","description":"aye","inputSchema":{"type":"object"}},{"description":"no name","inputSchema":{}},{"name":"skip","inputSchema":null},7],"nextCursor":"p2"}}`+"\n")
	case "tools/call":
		if strings.Contains(raw, `"missing"`) {
			io.WriteString(w, `{"jsonrpc":"2.0","id":`+id+`,"error":{"code":-32602,"message":"unknown tool"}}`+"\n")
			return
		}
		if strings.Contains(raw, `"n":1`) {
			io.WriteString(w, `{"jsonrpc":"2.0","id":`+id+`,"result":{"content":[{"type":"text","text":"no"}],"isError":true}}`+"\n")
			return
		}
		io.WriteString(w, `{"jsonrpc":"2.0","id":`+id+`,"result":{"content":[{"type":"text","text":"hi"}],"isError":false}}`+"\n")
	}
}

func mustParams(t *testing.T, line string) []byte {
	t.Helper()
	var envelope struct {
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal([]byte(line), &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Params
}

func idOf(body []byte) string {
	var envelope struct {
		ID json.RawMessage `json:"id"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return "0"
	}
	return string(bytes.TrimSpace(envelope.ID))
}

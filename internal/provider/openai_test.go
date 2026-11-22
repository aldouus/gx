package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeSSE(t *testing.T) {
	body := "" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"function\":{\"name\":\"shell\",\"arguments\":\"{\\\"\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"cmd\\\":\\\"a\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: [DONE]\n"
	var texts []string
	var args string
	err := DecodeSSE(strings.NewReader(body), func(d Delta) error {
		texts = append(texts, d.Text)
		if d.Call != nil {
			args += d.Call.Args
			if d.Call.Name != "" && d.Call.Name != "shell" && d.Call.Index == 0 {
				t.Fatal(d.Call.Name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(texts, "") != "hi" && texts[0] != "hi" {
		t.Fatalf("texts %q", texts)
	}
	if !strings.Contains(args, "cmd") {
		t.Fatalf("args %s", args)
	}
}

func TestStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test" {
			http.Error(w, "auth", 401)
			return
		}
		b, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(b), `"stream":true`) {
			http.Error(w, "stream", 400)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
		io.WriteString(w, "data: [DONE]\n")
	}))
	defer srv.Close()
	p := OpenAI{BaseURL: srv.URL, APIKey: "test"}
	ch, err := p.Stream(context.Background(), Request{Model: "m", Messages: []Message{{Role: "user", Text: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	var got string
	for d := range ch {
		got += d.Text
	}
	if got != "ok" {
		t.Fatalf("got %q", got)
	}
}

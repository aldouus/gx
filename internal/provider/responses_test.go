package provider

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResponsesTextAndToolCall(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("chatgpt-account-id") != "acct" || r.Header.Get("originator") != "fx" {
			http.Error(w, "headers", http.StatusBadRequest)
			return
		}
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		got = string(body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"read_file\",\"arguments\":\"\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":0,\"delta\":\"{\\\"path\\\":\\\"a\\\"}\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.function_call_arguments.done\",\"output_index\":0,\"arguments\":\"{\\\"path\\\":\\\"a\\\"}\"}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()
	p := Responses{Kind: "codex", URL: srv.URL, APIKey: "tok", AccountID: "acct"}
	ch, err := p.Stream(t.Context(), Request{
		Model: "gpt-5.1-codex",
		Messages: []Message{
			{Role: "system", Text: "be brief"},
			{Role: "user", Text: "read a"},
		},
		Tools: []Tool{{Name: "read_file", Description: "Read", Schema: []byte(`{"type":"object"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var text, args, name string
	for d := range ch {
		text += d.Text
		if d.Call != nil {
			if d.Call.Name != "" {
				name = d.Call.Name
			}
			args += d.Call.Args
		}
	}
	if text != "hi" || name != "read_file" || args != `{"path":"a"}` {
		t.Fatalf("text %q name %q args %q", text, name, args)
	}
	if !strings.Contains(got, `"instructions":"be brief"`) || !strings.Contains(got, `"input_text"`) {
		t.Fatal(got)
	}
	if !strings.Contains(got, `"parameters":{"type":"object"}`) {
		t.Fatal(got)
	}
}

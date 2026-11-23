// Package provider streams one model. Provider-specific JSON stays in this
// package. The agent sees text and tool-call deltas.
package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Tool is a function the model may call.
type Tool struct {
	Name        string
	Description string
	Schema      json.RawMessage
}

// ToolCall is one function call, with arguments accumulated across deltas.
type ToolCall struct {
	ID, Name, Args string
}

// Message is one chat message. Parts carries images when Text is not enough.
type Message struct {
	Role      string
	Text      string
	Parts     []Part
	ToolCalls []ToolCall
	ToolID    string
}

// Part is a text or image block.
type Part struct {
	Text     string
	ImageURL string
}

// Request is one streamed completion.
type Request struct {
	Model       string
	Messages    []Message
	Tools       []Tool
	ServiceTier string
	// Reasoning is prior Responses reasoning items, including encrypted_content.
	Reasoning []json.RawMessage
}

// Delta is one piece of the stream.
type Delta struct {
	Text      string
	Call      *CallDelta
	Reasoning json.RawMessage
	Done      bool
}

// CallDelta is one fragment of a tool call.
type CallDelta struct {
	Index    int
	ID, Name string
	Args     string
}

// Provider streams a completion.
type Provider interface {
	Stream(ctx context.Context, req Request) (<-chan Delta, error)
}

// OpenAI is an OpenAI-compatible Chat Completions client.
type OpenAI struct {
	BaseURL string
	APIKey  string
	Client  *http.Client
}

func (o OpenAI) endpoint() string {
	base := strings.TrimRight(o.BaseURL, "/")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	return base + "/chat/completions"
}

func (o OpenAI) client() *http.Client {
	if o.Client != nil {
		return o.Client
	}
	return http.DefaultClient
}

// Stream posts a streaming chat completion and decodes the SSE body.
func (o OpenAI) Stream(ctx context.Context, req Request) (<-chan Delta, error) {
	body, err := json.Marshal(encode(req))
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.endpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if o.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+o.APIKey)
	}
	res, err := o.client().Do(httpReq)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		res.Body.Close()
		return nil, fmt.Errorf("provider status %d: %s", res.StatusCode, strings.TrimSpace(string(b)))
	}
	ch := make(chan Delta)
	go func() {
		defer close(ch)
		defer res.Body.Close()
		if err := DecodeSSE(res.Body, func(d Delta) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case ch <- d:
				return nil
			}
		}); err != nil && ctx.Err() == nil {
			select {
			case <-ctx.Done():
			case ch <- Delta{Done: true, Text: ""}:
			}
			_ = err
			select {
			case <-ctx.Done():
			case ch <- Delta{Text: "", Done: true}:
			}
		}
	}()
	return ch, nil
}

type wireReq struct {
	Model    string     `json:"model"`
	Stream   bool       `json:"stream"`
	Messages []wireMsg  `json:"messages"`
	Tools    []wireTool `json:"tools,omitempty"`
}

type wireMsg struct {
	Role       string     `json:"role"`
	Content    any        `json:"content,omitempty"`
	ToolCalls  []wireCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type wirePart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL *struct {
		URL string `json:"url"`
	} `json:"image_url,omitempty"`
}

type wireCall struct {
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

type wireTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

func encode(req Request) wireReq {
	out := wireReq{Model: req.Model, Stream: true}
	for _, m := range req.Messages {
		wm := wireMsg{Role: m.Role, ToolCallID: m.ToolID}
		if len(m.Parts) > 0 {
			var parts []wirePart
			for _, p := range m.Parts {
				if p.ImageURL != "" {
					parts = append(parts, wirePart{Type: "image_url", ImageURL: &struct {
						URL string `json:"url"`
					}{URL: p.ImageURL}})
					continue
				}
				parts = append(parts, wirePart{Type: "text", Text: p.Text})
			}
			wm.Content = parts
		} else {
			wm.Content = m.Text
		}
		for _, c := range m.ToolCalls {
			wc := wireCall{ID: c.ID, Type: "function"}
			wc.Function.Name = c.Name
			wc.Function.Arguments = c.Args
			wm.ToolCalls = append(wm.ToolCalls, wc)
		}
		out.Messages = append(out.Messages, wm)
	}
	for _, t := range req.Tools {
		schema := t.Schema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		wt := wireTool{Type: "function"}
		wt.Function.Name = t.Name
		wt.Function.Description = t.Description
		wt.Function.Parameters = schema
		out.Tools = append(out.Tools, wt)
	}
	return out
}

// DecodeSSE reads a chat-completions event stream.
func DecodeSSE(r io.Reader, emit func(Delta) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			return emit(Delta{Done: true})
		}
		var chunk struct {
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return err
		}
		if chunk.Error != nil {
			return fmt.Errorf("%s", chunk.Error.Message)
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				if err := emit(Delta{Text: choice.Delta.Content}); err != nil {
					return err
				}
			}
			for _, call := range choice.Delta.ToolCalls {
				if err := emit(Delta{Call: &CallDelta{
					Index: call.Index,
					ID:    call.ID,
					Name:  call.Function.Name,
					Args:  call.Function.Arguments,
				}}); err != nil {
					return err
				}
			}
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				if err := emit(Delta{Done: true}); err != nil {
					return err
				}
			}
		}
	}
	return sc.Err()
}

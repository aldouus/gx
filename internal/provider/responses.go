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

const (
	// CodexURL is the ChatGPT Codex responses endpoint.
	CodexURL = "https://chatgpt.com/backend-api/codex/responses"
	// GrokURL is the xAI Grok subscription responses endpoint.
	GrokURL = "https://cli-chat-proxy.grok.com/v1/responses"
)

// Responses streams the OpenAI Responses API used by Codex and Grok subscriptions.
type Responses struct {
	Kind      string // "codex" or "grok"
	URL       string
	APIKey    string
	AccountID string
	Client    *http.Client
}

func (r Responses) endpoint() string {
	if r.URL != "" {
		return r.URL
	}
	if r.Kind == "grok" {
		return GrokURL
	}
	return CodexURL
}

func (r Responses) client() *http.Client {
	if r.Client != nil {
		return r.Client
	}
	return http.DefaultClient
}

// Stream posts one Responses request and emits text and tool-call deltas.
func (r Responses) Stream(ctx context.Context, req Request) (<-chan Delta, error) {
	body, err := json.Marshal(encodeResponses(req))
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if r.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+r.APIKey)
	}
	switch r.Kind {
	case "grok":
		httpReq.Header.Set("X-XAI-Token-Auth", "xai-grok-cli")
		httpReq.Header.Set("x-authenticateresponse", "authenticate-response")
		httpReq.Header.Set("x-grok-client-identifier", "fx")
		httpReq.Header.Set("x-grok-model-override", req.Model)
		if r.AccountID != "" {
			httpReq.Header.Set("x-grok-user-id", r.AccountID)
		}
	default:
		httpReq.Header.Set("originator", "fx")
		httpReq.Header.Set("OpenAI-Beta", "responses=experimental")
		if r.AccountID != "" {
			httpReq.Header.Set("chatgpt-account-id", r.AccountID)
		}
	}
	res, err := r.client().Do(httpReq)
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
		_ = DecodeResponses(res.Body, func(d Delta) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case ch <- d:
				return nil
			}
		})
	}()
	return ch, nil
}

func encodeResponses(req Request) map[string]any {
	var instructions []string
	var input []any
	for _, m := range req.Messages {
		switch m.Role {
		case "system":
			if strings.TrimSpace(m.Text) != "" {
				instructions = append(instructions, m.Text)
			}
		case "user":
			input = append(input, map[string]any{
				"role":    "user",
				"content": responseUserContent(m),
			})
		case "assistant":
			if m.Text != "" {
				input = append(input, map[string]any{
					"type":   "message",
					"role":   "assistant",
					"status": "completed",
					"content": []any{
						map[string]any{"type": "output_text", "text": m.Text, "annotations": []any{}},
					},
				})
			}
			for _, c := range m.ToolCalls {
				input = append(input, map[string]any{
					"type":      "function_call",
					"call_id":   c.ID,
					"name":      c.Name,
					"arguments": c.Args,
				})
			}
		case "tool":
			input = append(input, map[string]any{
				"type":    "function_call_output",
				"call_id": m.ToolID,
				"output":  m.Text,
			})
		}
	}
	if len(instructions) == 0 {
		instructions = []string{"You are a helpful assistant."}
	}
	body := map[string]any{
		"model":               req.Model,
		"store":               false,
		"stream":              true,
		"instructions":        strings.Join(instructions, "\n\n"),
		"input":               input,
		"parallel_tool_calls": true,
	}
	if len(req.Tools) > 0 {
		var tools []any
		for _, t := range req.Tools {
			schema := t.Schema
			if len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object"}`)
			}
			tools = append(tools, map[string]any{
				"type":        "function",
				"name":        t.Name,
				"description": t.Description,
				"parameters":  schema,
				"strict":      false,
			})
		}
		body["tools"] = tools
		body["tool_choice"] = "auto"
	}
	return body
}

func responseUserContent(m Message) []any {
	var content []any
	if len(m.Parts) == 0 {
		content = append(content, map[string]any{"type": "input_text", "text": m.Text})
		return content
	}
	for _, p := range m.Parts {
		if p.ImageURL != "" {
			content = append(content, map[string]any{"type": "input_image", "image_url": p.ImageURL})
			continue
		}
		content = append(content, map[string]any{"type": "input_text", "text": p.Text})
	}
	if len(content) == 0 {
		content = append(content, map[string]any{"type": "input_text", "text": m.Text})
	}
	return content
}

// DecodeResponses reads Responses SSE events and emits text and tool-call deltas.
func DecodeResponses(r io.Reader, emit func(Delta) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 8<<20)
	var data strings.Builder
	seenArgs := map[int]bool{}
	flush := func() error {
		payload := strings.TrimSpace(data.String())
		data.Reset()
		if payload == "" || payload == "[DONE]" {
			return nil
		}
		return emitResponses(payload, seenArgs, emit)
	}
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return flush()
}

func emitResponses(payload string, seenArgs map[int]bool, emit func(Delta) error) error {
	var ev struct {
		Type        string `json:"type"`
		Delta       string `json:"delta"`
		Arguments   string `json:"arguments"`
		OutputIndex int    `json:"output_index"`
		Item        struct {
			Type      string `json:"type"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"item"`
	}
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		return nil
	}
	switch ev.Type {
	case "response.output_text.delta":
		if ev.Delta != "" {
			return emit(Delta{Text: ev.Delta})
		}
	case "response.output_item.added":
		if ev.Item.Type == "function_call" {
			return emit(Delta{Call: &CallDelta{
				Index: ev.OutputIndex,
				ID:    ev.Item.CallID,
				Name:  ev.Item.Name,
				Args:  ev.Item.Arguments,
			}})
		}
	case "response.function_call_arguments.delta":
		if ev.Delta != "" {
			seenArgs[ev.OutputIndex] = true
			return emit(Delta{Call: &CallDelta{Index: ev.OutputIndex, Args: ev.Delta}})
		}
	case "response.function_call_arguments.done":
		if ev.Arguments != "" && !seenArgs[ev.OutputIndex] {
			return emit(Delta{Call: &CallDelta{Index: ev.OutputIndex, Args: ev.Arguments}})
		}
	}
	return nil
}

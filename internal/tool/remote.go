package tool

import (
	"context"
	"encoding/json"
	"errors"
)

// Remote is a tool whose body runs somewhere else, usually an MCP server.
type Remote struct {
	ToolName string
	Blurb    string
	Input    json.RawMessage
	Call     func(ctx context.Context, args json.RawMessage) (string, error)
}

func (r Remote) Name() string { return r.ToolName }
func (r Remote) Description() string {
	if r.Blurb == "" {
		return "MCP tool " + r.ToolName
	}
	return r.Blurb
}
func (r Remote) Schema() json.RawMessage {
	if len(r.Input) == 0 {
		return json.RawMessage(`{"type":"object"}`)
	}
	return r.Input
}
func (Remote) ReadOnly() bool      { return false }
func (Remote) NeedsApproval() bool { return true }

func (r Remote) Run(ctx context.Context, args json.RawMessage) (string, error) {
	if r.Call == nil {
		return "", errors.New("mcp tool unavailable")
	}
	return r.Call(ctx, args)
}

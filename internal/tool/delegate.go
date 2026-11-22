package tool

import (
	"context"
	"encoding/json"
	"fmt"

	"gx/internal/skills"
)

type skillTool struct{ list []skills.Skill }

// Skill loads one discovered skill's instructions.
func Skill(list []skills.Skill) Tool { return skillTool{list: list} }

func (skillTool) Name() string { return "skill" }
func (skillTool) Description() string {
	return "Load a skill's full instructions by name. Discover the name from the skills list in the prompt."
}
func (skillTool) ReadOnly() bool      { return true }
func (skillTool) NeedsApproval() bool { return false }
func (skillTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"],"additionalProperties":false}`)
}

func (t skillTool) Run(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	for _, sk := range t.list {
		if sk.Name == in.Name {
			return skills.Load(sk.Path)
		}
	}
	return "", fmt.Errorf("skill %q not found", in.Name)
}

// Spawn runs a child agent. The parent keeps working on other tool calls.
type Spawn func(ctx context.Context, message string, depth int) (string, error)

type delegate struct{ spawn Spawn }

// Subagent is the subagent tool. Nesting stops at depth two.
func Subagent(spawn Spawn) Tool { return delegate{spawn: spawn} }

func (delegate) Name() string { return "subagent" }
func (delegate) Description() string {
	return "Delegate a task to a subagent. It reports when its work ends. A subagent's subagent may run; further nesting stops."
}
func (delegate) ReadOnly() bool      { return false }
func (delegate) NeedsApproval() bool { return false }
func (delegate) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"action":{"type":"string","enum":["message"]},"message":{"type":"string"},"agent":{"type":"string"}},"required":["message"],"additionalProperties":false}`)
}

func (d delegate) Run(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Action  string `json:"action"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	if in.Action != "" && in.Action != "message" {
		return "", fmt.Errorf("subagent action %q is not supported", in.Action)
	}
	depth := Depth(ctx)
	if depth >= 2 {
		return "", fmt.Errorf("nesting stops at two")
	}
	if d.spawn == nil {
		return "", fmt.Errorf("subagent is unavailable")
	}
	return d.spawn(ctx, in.Message, depth)
}

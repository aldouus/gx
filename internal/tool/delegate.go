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

// Agents is the subagent tool's view of the children it started.
type Agents interface {
	Message(ctx context.Context, message string, depth int) (string, error)
	Wait(ctx context.Context, name string) (string, error)
	Status(name string) string
	Stop(name string) string
	Close(name string) string
}

// AgentCall is a subagent request with an optional persistent name, model,
// profile, and a parent destination.
type AgentCall struct {
	Message, Agent, Model, Profile, To string
	Depth                              int
}

// Sender starts or continues a subagent with AgentCall.
type Sender interface {
	Send(ctx context.Context, call AgentCall) (string, error)
}

type delegate struct{ agents Agents }

// Subagent is the subagent tool. Nesting stops at depth two.
func Subagent(agents Agents) Tool { return delegate{agents: agents} }

func (delegate) Name() string { return "subagent" }
func (delegate) Description() string {
	return "Start a subagent, wait for its report, or stop it. action is message, wait, status, stop, or close. message starts one and returns at once. to=parent sends the message to the parent. A named agent is persistent. wait blocks until a report. stop ends a running turn. close stops it and drops it."
}
func (delegate) ReadOnly() bool      { return false }
func (delegate) NeedsApproval() bool { return false }
func (delegate) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"action":{"type":"string","enum":["message","wait","status","stop","close"]},"message":{"type":"string"},"agent":{"type":"string"},"model":{"type":"string"},"profile":{"type":"string"},"to":{"type":"string"}},"additionalProperties":false}`)
}

func (d delegate) Run(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Action  string `json:"action"`
		Message string `json:"message"`
		Agent   string `json:"agent"`
		Model   string `json:"model"`
		Profile string `json:"profile"`
		To      string `json:"to"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	if in.Action == "" {
		in.Action = "message"
	}
	if d.agents == nil {
		return "", fmt.Errorf("subagent is unavailable")
	}
	switch in.Action {
	case "message":
		if in.Message == "" {
			return "", fmt.Errorf("subagent message is required")
		}
		depth := Depth(ctx)
		if depth >= 2 {
			return "", fmt.Errorf("nesting stops at two")
		}
		if sender, ok := d.agents.(Sender); ok {
			return sender.Send(ctx, AgentCall{
				Message: in.Message,
				Agent:   in.Agent,
				Model:   in.Model,
				Profile: in.Profile,
				To:      in.To,
				Depth:   depth,
			})
		}
		return d.agents.Message(ctx, in.Message, depth)
	case "wait":
		return d.agents.Wait(ctx, in.Agent)
	case "status":
		return d.agents.Status(in.Agent), nil
	case "stop":
		if in.Agent == "" {
			return "", fmt.Errorf("subagent stop needs an agent name")
		}
		return d.agents.Stop(in.Agent), nil
	case "close":
		if in.Agent == "" {
			return "", fmt.Errorf("subagent close needs an agent name")
		}
		return d.agents.Close(in.Agent), nil
	default:
		return "", fmt.Errorf("subagent action %q is not supported", in.Action)
	}
}

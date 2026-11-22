package agent

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"gx/internal/compact"
	"gx/internal/perm"
	"gx/internal/provider"
	"gx/internal/session"
	"gx/internal/tool"
)

//go:embed system_prompt.md
var basePrompt string

// Event is one thing the transcript prints, in order.
type Event struct {
	Kind   string
	Name   string
	Text   string
	Detail string
}

// Approval asks the person. Returning false denies the call.
type Approval func(ctx context.Context, name, detail string) (bool, error)

// Reviewer is the auto-mode check. allow true runs the call. allow false
// holds it for Approval.
type Reviewer func(ctx context.Context, name, detail string) (allow bool, reason string, err error)

// Runner is one turn of the agent loop.
type Runner struct {
	Provider  provider.Provider
	Tools     *tool.Registry
	Mode      perm.Mode
	Model     string
	Session   *session.Session
	Approve   Approval
	Review    Reviewer
	Workspace string
	Extra     string
	CompactAt int
	MaxRounds int
	Depth     int
}

// Turn sends prompt, streams the reply, runs tool calls, and commits the turn.
func (r *Runner) Turn(ctx context.Context, prompt string, emit func(Event)) (string, error) {
	if r.MaxRounds == 0 {
		r.MaxRounds = 64
	}
	if r.CompactAt == 0 {
		r.CompactAt = 120_000
	}
	ctx = tool.WithDepth(ctx, r.Depth)
	if _, err := r.Session.User(prompt); err != nil {
		return "", err
	}
	var final string
	for round := 0; round < r.MaxRounds; round++ {
		if err := ctx.Err(); err != nil {
			_ = r.Session.Interrupt()
			return final, err
		}
		if err := r.maybeCompact(ctx, emit); err != nil {
			_ = r.Session.Interrupt()
			return final, err
		}
		text, calls, err := r.complete(ctx, emit)
		if err != nil {
			_ = r.Session.Interrupt()
			return final, err
		}
		final = text
		if len(calls) == 0 {
			if err := r.Session.Commit(); err != nil {
				return final, err
			}
			return final, nil
		}
		if err := r.runTools(ctx, calls, emit); err != nil {
			_ = r.Session.Interrupt()
			return final, err
		}
	}
	_ = r.Session.Interrupt()
	return final, fmt.Errorf("agent stopped after %d rounds", r.MaxRounds)
}

func (r *Runner) complete(ctx context.Context, emit func(Event)) (string, []provider.ToolCall, error) {
	req := provider.Request{
		Model:    r.Model,
		Messages: r.messages(),
		Tools:    r.toolDefs(),
	}
	ch, err := r.Provider.Stream(ctx, req)
	if err != nil {
		return "", nil, err
	}
	var text strings.Builder
	calls := map[int]*provider.ToolCall{}
	var order []int
	for d := range ch {
		if err := ctx.Err(); err != nil {
			return text.String(), nil, err
		}
		if d.Text != "" {
			text.WriteString(d.Text)
			if err := r.Session.Assistant(d.Text); err != nil {
				return text.String(), nil, err
			}
			if emit != nil {
				emit(Event{Kind: "text", Text: d.Text})
			}
		}
		if d.Call != nil {
			c := calls[d.Call.Index]
			if c == nil {
				c = &provider.ToolCall{}
				calls[d.Call.Index] = c
				order = append(order, d.Call.Index)
			}
			if d.Call.ID != "" {
				c.ID = d.Call.ID
			}
			if d.Call.Name != "" {
				c.Name = d.Call.Name
			}
			c.Args += d.Call.Args
		}
	}
	if emit != nil && text.Len() > 0 {
		emit(Event{Kind: "text_done", Text: text.String()})
	}
	var out []provider.ToolCall
	for _, i := range order {
		c := calls[i]
		if c.ID == "" {
			c.ID = fmt.Sprintf("call_%d", i)
		}
		out = append(out, *c)
	}
	return text.String(), out, nil
}

func (r *Runner) runTools(ctx context.Context, calls []provider.ToolCall, emit func(Event)) error {
	type result struct {
		call provider.ToolCall
		text string
		err  error
	}
	allowed := make([]bool, len(calls))
	for i, call := range calls {
		if err := r.Session.ToolCall(call.ID, call.Name, call.Args); err != nil {
			return err
		}
		ok, err := r.allow(ctx, call, emit)
		if err != nil {
			return err
		}
		allowed[i] = ok
		if emit != nil {
			emit(Event{Kind: "tool_start", Name: call.Name, Detail: call.Args})
		}
	}
	results := make([]result, len(calls))
	var wg sync.WaitGroup
	var mu sync.Mutex
	errOnce := error(nil)
	for i, call := range calls {
		if !allowed[i] {
			results[i] = result{call: call, text: "permission denied"}
			continue
		}
		wg.Add(1)
		go func(i int, call provider.ToolCall) {
			defer wg.Done()
			t, ok := r.Tools.Get(call.Name)
			var text string
			var err error
			if !ok {
				text = "unknown tool " + call.Name
			} else {
				text, err = t.Run(ctx, json.RawMessage(call.Args))
			}
			mu.Lock()
			defer mu.Unlock()
			if err != nil && errOnce == nil && ctx.Err() != nil {
				errOnce = err
			}
			if err != nil && text == "" {
				text = err.Error()
			}
			results[i] = result{call: call, text: text, err: err}
		}(i, call)
	}
	wg.Wait()
	if errOnce != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	for _, res := range results {
		if err := r.Session.ToolResult(res.call.ID, res.call.Name, res.text); err != nil {
			return err
		}
		if emit != nil {
			emit(Event{Kind: "tool_done", Name: res.call.Name, Text: res.text, Detail: res.call.Args})
		}
	}
	return nil
}

func (r *Runner) allow(ctx context.Context, call provider.ToolCall, emit func(Event)) (bool, error) {
	t, ok := r.Tools.Get(call.Name)
	spec := perm.Spec{Name: call.Name, ShellCommand: shellCommand(call)}
	if ok {
		spec.NeedsApproval = t.NeedsApproval()
	}
	if call.Name == "edit_file" || call.Name == "write_file" {
		spec.WorkspaceEdit = pathInWorkspace(r.Workspace, call.Args)
	}
	spec.Routine = perm.Routine(spec)
	switch perm.Decide(r.Mode, spec) {
	case perm.Allow:
		return true, nil
	case perm.Review:
		if r.Review != nil {
			allow, _, err := r.Review(ctx, call.Name, call.Args)
			if err != nil {
				return false, err
			}
			if allow {
				return true, nil
			}
		}
		fallthrough
	case perm.Prompt:
		if emit != nil {
			emit(Event{Kind: "permission", Name: call.Name, Detail: call.Args})
		}
		if r.Approve == nil {
			return false, nil
		}
		return r.Approve(ctx, call.Name, call.Args)
	default:
		return false, nil
	}
}

func shellCommand(call provider.ToolCall) string {
	var m map[string]any
	_ = json.Unmarshal([]byte(call.Args), &m)
	s, _ := m["command"].(string)
	return s
}

func pathInWorkspace(root, args string) bool {
	if root == "" {
		return false
	}
	var m map[string]any
	_ = json.Unmarshal([]byte(args), &m)
	p, _ := m["path"].(string)
	if p == "" {
		return false
	}
	if strings.HasPrefix(p, "/") && !strings.HasPrefix(p, root) {
		return false
	}
	return true
}

func (r *Runner) messages() []provider.Message {
	sys := strings.TrimSpace(basePrompt)
	if r.Extra != "" {
		sys += "\n\n" + r.Extra
	}
	out := []provider.Message{{Role: "system", Text: sys}}
	for _, m := range r.Session.History() {
		pm := provider.Message{Role: m.Role, Text: m.Text, ToolID: m.ToolID}
		for _, c := range m.ToolCalls {
			pm.ToolCalls = append(pm.ToolCalls, provider.ToolCall{ID: c.ID, Name: c.Name, Args: c.Args})
		}
		out = append(out, pm)
	}
	return out
}

func (r *Runner) toolDefs() []provider.Tool {
	if r.Tools == nil {
		return nil
	}
	var defs []provider.Tool
	for _, t := range r.Tools.All() {
		defs = append(defs, provider.Tool{Name: t.Name(), Description: t.Description(), Schema: t.Schema()})
	}
	return defs
}

func (r *Runner) maybeCompact(ctx context.Context, emit func(Event)) error {
	hist := r.Session.History()
	n := 0
	for _, m := range hist {
		n += len(m.Text)
	}
	if n < r.CompactAt || r.Provider == nil {
		return nil
	}
	var row compact.Progress
	row.Begin()
	row.Summarize()
	row.Ask(1)
	if emit != nil {
		emit(Event{Kind: "compact", Text: fmt.Sprintf("Compacting %d%%", row.Shown())})
	}
	req := provider.Request{
		Model: r.Model,
		Messages: []provider.Message{
			{Role: "system", Text: "Summarize the conversation so far. Preserve the user's current intent, the latest tool results, unresolved blockers, and verification state. Be concise."},
			{Role: "user", Text: r.Session.TranscriptText()},
		},
	}
	ch, err := r.Provider.Stream(ctx, req)
	if err != nil {
		return err
	}
	var summary strings.Builder
	notes := 0
	for d := range ch {
		if d.Text != "" {
			summary.WriteString(d.Text)
			lines := strings.Count(summary.String(), "\n") + 1
			if lines > notes {
				notes = lines
				row.Write(1, notes)
				if emit != nil {
					emit(Event{Kind: "compact", Text: fmt.Sprintf("Compacting %d%%", row.Shown())})
				}
			}
		}
	}
	if notes == 0 {
		notes = 1
		row.Write(1, notes)
	}
	row.Save()
	if emit != nil {
		emit(Event{Kind: "compact", Text: fmt.Sprintf("Compacting %d%%", row.Shown())})
	}
	if err := r.Session.Compaction(summary.String(), 0); err != nil {
		return err
	}
	row.Settle()
	return nil
}

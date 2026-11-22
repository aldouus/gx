package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"gx/internal/perm"
	"gx/internal/provider"
	"gx/internal/session"
	"gx/internal/tool"
)

type script struct {
	steps [][]provider.Delta
	seen  []provider.Request
	n     int
}

func (s *script) Stream(ctx context.Context, req provider.Request) (<-chan provider.Delta, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.seen = append(s.seen, req)
	ch := make(chan provider.Delta)
	var step []provider.Delta
	if s.n < len(s.steps) {
		step = s.steps[s.n]
	}
	s.n++
	go func() {
		defer close(ch)
		for _, d := range step {
			select {
			case <-ctx.Done():
				return
			case ch <- d:
			}
		}
	}()
	return ch, nil
}

func TestTurnToolThenText(t *testing.T) {
	root := t.TempDir()
	s, err := session.Create(root, root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	dir := t.TempDir()
	ws := tool.Workspace{Roots: []string{dir}}
	prov := &script{steps: [][]provider.Delta{
		{
			{Text: "looking"},
			{Call: &provider.CallDelta{Index: 0, ID: "c1", Name: "shell", Args: `{"action":"run","command":"printf hi","profile":"clean"}`}},
			{Done: true},
		},
		{
			{Text: "done"},
			{Done: true},
		},
	}}
	var kinds []string
	r := &Runner{
		Provider:  prov,
		Tools:     tool.Builtin(ws),
		Mode:      perm.FullAccess,
		Model:     "test",
		Session:   s,
		Workspace: dir,
	}
	out, err := r.Turn(context.Background(), "say hi", func(ev Event) {
		kinds = append(kinds, ev.Kind)
	})
	if err != nil {
		t.Fatal(err)
	}
	if out != "done" {
		t.Fatalf("out %q", out)
	}
	joined := strings.Join(kinds, ",")
	textAt := strings.Index(joined, "text")
	toolAt := strings.Index(joined, "tool_start")
	if textAt < 0 || toolAt < 0 || textAt > toolAt {
		t.Fatalf("order %s", joined)
	}
	found := false
	for _, ev := range s.Events() {
		if ev.Kind == "turn_committed" {
			found = true
		}
	}
	if !found {
		t.Fatal("turn was not committed")
	}
}

type reportTool struct{ box *Mailbox }

func (reportTool) Name() string        { return "subagent" }
func (reportTool) Description() string { return "child" }
func (reportTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (reportTool) ReadOnly() bool      { return false }
func (reportTool) NeedsApproval() bool { return false }
func (t reportTool) Run(_ context.Context, _ json.RawMessage) (string, error) {
	t.box.Start("child-1", nil)
	t.box.Finish("child-1", "report body")
	return "subagent child-1 started", nil
}

func TestReportJoinsTheOpenTurn(t *testing.T) {
	root := t.TempDir()
	s, err := session.Create(root, root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	box := &Mailbox{}
	reg := tool.NewRegistry(reportTool{box: box})
	prov := &script{steps: [][]provider.Delta{
		{{Call: &provider.CallDelta{Index: 0, ID: "c1", Name: "subagent", Args: `{"message":"go"}`}}, {Done: true}},
		{{Text: "saw the report"}, {Done: true}},
	}}
	r := &Runner{
		Provider: prov,
		Tools:    reg,
		Mode:     perm.FullAccess,
		Model:    "test",
		Session:  s,
		Inbox:    box,
	}
	out, err := r.Turn(context.Background(), "delegate", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "saw the report" {
		t.Fatalf("out %q", out)
	}
	if len(prov.seen) < 2 || !strings.Contains(requestText(prov.seen[1]), "report body") {
		t.Fatalf("second request did not see the report: %#v", prov.seen)
	}
}

func requestText(req provider.Request) string {
	var b strings.Builder
	for _, m := range req.Messages {
		b.WriteString(m.Text)
		b.WriteByte('\n')
	}
	return b.String()
}

func TestAskDeniesShell(t *testing.T) {
	root := t.TempDir()
	s, err := session.Create(root, root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	prov := &script{steps: [][]provider.Delta{
		{
			{Call: &provider.CallDelta{Index: 0, ID: "c1", Name: "shell", Args: `{"action":"run","command":"printf no","profile":"clean"}`}},
			{Done: true},
		},
		{{Text: "denied"}, {Done: true}},
	}}
	var prompted bool
	r := &Runner{
		Provider: prov,
		Tools:    tool.Builtin(tool.Workspace{Roots: []string{t.TempDir()}}),
		Mode:     perm.Ask,
		Model:    "test",
		Session:  s,
		Approve: func(context.Context, string, string) (Verdict, error) {
			prompted = true
			return Deny, nil
		},
	}
	if _, err := r.Turn(context.Background(), "run it", nil); err != nil {
		t.Fatal(err)
	}
	if !prompted {
		t.Fatal("ask mode did not prompt")
	}
}

func TestCancelDoesNotCommit(t *testing.T) {
	root := t.TempDir()
	s, err := session.Create(root, root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	prov := &script{steps: [][]provider.Delta{
		{{Text: "nope"}},
	}}
	r := &Runner{
		Provider: prov,
		Tools:    tool.NewRegistry(),
		Mode:     perm.Ask,
		Model:    "test",
		Session:  s,
	}
	cancel()
	_, err = r.Turn(ctx, "stop", nil)
	if err == nil {
		t.Fatal("expected cancel")
	}
	for _, ev := range s.Events() {
		if ev.Kind == "turn_committed" {
			t.Fatal("cancelled turn was committed")
		}
	}
	var interrupted bool
	for _, ev := range s.Events() {
		if ev.Kind == "turn_interrupted" {
			interrupted = true
		}
	}
	if !interrupted {
		t.Fatal("cancelled turn was not interrupted")
	}
}

func TestAutoReviewsShell(t *testing.T) {
	root := t.TempDir()
	s, err := session.Create(root, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	prov := &script{steps: [][]provider.Delta{
		{{Call: &provider.CallDelta{Index: 0, ID: "c1", Name: "shell", Args: `{"action":"run","command":"printf x","profile":"clean"}`}}, {Done: true}},
		{{Text: "ok"}, {Done: true}},
	}}
	reviewed := false
	r := &Runner{
		Provider: prov,
		Tools:    tool.Builtin(tool.Workspace{Roots: []string{t.TempDir()}}),
		Mode:     perm.Auto,
		Model:    "test",
		Session:  s,
		Review: func(context.Context, string, string) (bool, string, error) {
			reviewed = true
			return false, "hold", nil
		},
		Approve: func(context.Context, string, string) (Verdict, error) { return Deny, nil },
	}
	if _, err := r.Turn(context.Background(), "x", nil); err != nil {
		t.Fatal(err)
	}
	if !reviewed {
		t.Fatal("auto mode did not review")
	}
	_ = json.RawMessage(nil)
}

func TestAllowAlwaysSkipsTheNextPrompt(t *testing.T) {
	root := t.TempDir()
	s, err := session.Create(root, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	args := `{"action":"run","command":"printf hi","profile":"clean"}`
	prov := &script{steps: [][]provider.Delta{
		{{Call: &provider.CallDelta{Index: 0, ID: "c1", Name: "shell", Args: args}}, {Done: true}},
		{{Text: "once"}, {Done: true}},
		{{Call: &provider.CallDelta{Index: 0, ID: "c2", Name: "shell", Args: args}}, {Done: true}},
		{{Text: "again"}, {Done: true}},
	}}
	prompts := 0
	r := &Runner{
		Provider: prov,
		Tools:    tool.Builtin(tool.Workspace{Roots: []string{t.TempDir()}}),
		Mode:     perm.Ask,
		Model:    "test",
		Session:  s,
		Grants:   &Grants{},
		Approve: func(context.Context, string, string) (Verdict, error) {
			prompts++
			return Always, nil
		},
	}
	if _, err := r.Turn(context.Background(), "first", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Turn(context.Background(), "second", nil); err != nil {
		t.Fatal(err)
	}
	if prompts != 1 {
		t.Fatalf("prompts = %d", prompts)
	}
}

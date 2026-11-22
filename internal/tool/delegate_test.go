package tool

import (
	"context"
	"encoding/json"
	"testing"
)

type fakeAgents struct {
	action string
}

func (f *fakeAgents) Message(context.Context, string, int) (string, error) {
	f.action = "message"
	return "started", nil
}
func (f *fakeAgents) Wait(context.Context, string) (string, error) {
	f.action = "wait"
	return "report", nil
}
func (f *fakeAgents) Status(string) string { f.action = "status"; return "idle" }
func (f *fakeAgents) Stop(string) string   { f.action = "stop"; return "stopped" }
func (f *fakeAgents) Close(string) string  { f.action = "close"; return "closed" }

func TestSubagentActions(t *testing.T) {
	f := &fakeAgents{}
	tool := Subagent(f)
	if _, err := tool.Run(context.Background(), json.RawMessage(`{"action":"wait"}`)); err != nil || f.action != "wait" {
		t.Fatalf("%s %v", f.action, err)
	}
	if _, err := tool.Run(context.Background(), json.RawMessage(`{"action":"stop"}`)); err == nil {
		t.Fatal("stop without a name")
	}
	if _, err := tool.Run(context.Background(), json.RawMessage(`{"action":"close","agent":"child-1"}`)); err != nil || f.action != "close" {
		t.Fatalf("%s %v", f.action, err)
	}
}

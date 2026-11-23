package agent

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestWaitWakesWhenContextEnds(t *testing.T) {
	var box Mailbox
	ctx, cancel := context.WithCancel(context.Background())
	box.Start("child-1", func() {})
	done := make(chan error, 1)
	go func() {
		done <- box.Wait(ctx)
	}()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("wait returned nil after cancel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("wait stayed blocked after the context ended")
	}
	box.Finish("child-1", "late")
}

func TestStopAndWait(t *testing.T) {
	var box Mailbox
	ctx, cancel := context.WithCancel(context.Background())
	box.Start("child-1", cancel)
	if !strings.Contains(box.Status(""), "child-1  running") {
		t.Fatal(box.Status(""))
	}
	done := make(chan string, 1)
	go func() {
		text, err := box.WaitReport(context.Background(), "child-1")
		if err != nil {
			done <- err.Error()
			return
		}
		done <- text
	}()
	if got := box.Stop("child-1"); got != "stopped child-1" {
		t.Fatal(got)
	}
	box.Finish("child-1", "halted")
	select {
	case got := <-done:
		if got != "halted" {
			t.Fatal(got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("wait did not return")
	}
	if box.Status("child-1") != "child-1  done  halted" {
		t.Fatal(box.Status("child-1"))
	}
	if got := box.Close("missing"); got != "no subagent missing" {
		t.Fatal(got)
	}
	if !strings.Contains(box.Close("child-1"), "child-1") {
		t.Fatal(box.Close("child-1"))
	}
	if ctx.Err() == nil {
		t.Fatal("stop did not cancel the child")
	}
}

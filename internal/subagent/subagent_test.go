package subagent

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"
)

func TestDepthRejected(t *testing.T) {
	called := false
	m := &Manager{Run: func(context.Context, Request, func(string)) (string, error) {
		called = true
		return "", nil
	}}
	for _, depth := range []int{2, 3, 9} {
		ch, err := m.Start(context.Background(), Request{Name: "grand", Depth: depth})
		if !errors.Is(err, ErrDepth) {
			t.Fatalf("depth %d: err %v", depth, err)
		}
		if ch != nil {
			t.Fatalf("depth %d: channel returned", depth)
		}
	}
	if called {
		t.Fatal("runner called")
	}

	for _, depth := range []int{0, 1} {
		ch, err := m.Start(context.Background(), Request{Name: "ok", Depth: depth})
		if err != nil {
			t.Fatalf("depth %d: %v", depth, err)
		}
		got := drain(t, ch)
		if len(got) != 1 || !got[0].Final {
			t.Fatalf("depth %d: %#v", depth, got)
		}
	}
}

func TestStartDoesNotBlock(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	m := &Manager{Run: func(context.Context, Request, func(string)) (string, error) {
		close(entered)
		<-release
		return "done", nil
	}}

	type result struct {
		ch  <-chan Report
		err error
	}
	started := make(chan result, 1)
	go func() {
		ch, err := m.Start(context.Background(), Request{Name: "child", Message: "work", Depth: 1})
		started <- result{ch, err}
	}()

	var res result
	select {
	case res = <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("Start blocked")
	}
	if res.err != nil {
		t.Fatal(res.err)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not start")
	}
	select {
	case rep := <-res.ch:
		t.Fatalf("report before the child finished: %#v", rep)
	default:
	}
	close(release)
	got := drain(t, res.ch)
	if len(got) != 1 || got[0].Text != "done" || !got[0].Final || got[0].Err != nil || got[0].Name != "child" {
		t.Fatalf("%#v", got)
	}
}

func TestFinalReport(t *testing.T) {
	for _, report := range []string{"", "final"} {
		m := &Manager{Run: func(_ context.Context, req Request, emit func(string)) (string, error) {
			if req.Name != "reviewer" || req.Message != "look" || req.Instructions != "be brief" || req.Report != report {
				t.Errorf("req %+v", req)
			}
			emit("not forwarded")
			return "found two bugs", nil
		}}
		ch, err := m.Start(context.Background(), Request{
			Name:         "reviewer",
			Message:      "look",
			Instructions: "be brief",
			Report:       report,
			Depth:        0,
		})
		if err != nil {
			t.Fatal(err)
		}
		got := drain(t, ch)
		if len(got) != 1 {
			t.Fatalf("report %q: %#v", report, got)
		}
		rep := got[0]
		if rep.Name != "reviewer" || rep.Text != "found two bugs" || !rep.Final || rep.Err != nil {
			t.Fatalf("report %q: %#v", report, rep)
		}
	}
}

func TestFailureReports(t *testing.T) {
	boom := errors.New("boom")
	for _, report := range []string{"final", "every_turn"} {
		m := &Manager{Run: func(_ context.Context, _ Request, emit func(string)) (string, error) {
			emit("step")
			return "partial", boom
		}}
		ch, err := m.Start(context.Background(), Request{Name: "reviewer", Report: report})
		if err != nil {
			t.Fatal(err)
		}
		got := drain(t, ch)
		last := got[len(got)-1]
		if !last.Final || last.Err != boom || last.Text != "partial" || last.Name != "reviewer" {
			t.Fatalf("report %q: %#v", report, got)
		}
		if report == "final" && len(got) != 1 {
			t.Fatalf("final forwarded emit: %#v", got)
		}
		if report == "every_turn" && (len(got) != 2 || got[0].Text != "step" || got[0].Final) {
			t.Fatalf("every_turn: %#v", got)
		}
	}
}

func TestCancelStopsRunner(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{})
	m := &Manager{Run: func(ctx context.Context, _ Request, _ func(string)) (string, error) {
		close(entered)
		<-ctx.Done()
		return "", ctx.Err()
	}}
	ch, err := m.Start(ctx, Request{Name: "child", Depth: 1})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not start")
	}
	cancel()
	got := drain(t, ch)
	if len(got) != 1 || !got[0].Final || !errors.Is(got[0].Err, context.Canceled) {
		t.Fatalf("%#v", got)
	}
}

func TestNeverReport(t *testing.T) {
	m := &Manager{Run: func(context.Context, Request, func(string)) (string, error) {
		return "ok", nil
	}}
	ch, err := m.Start(context.Background(), Request{Name: "child", Report: "never"})
	if err != nil {
		t.Fatal(err)
	}
	got := drain(t, ch)
	if len(got) != 0 {
		t.Fatalf("success reported %#v", got)
	}

	boom := errors.New("boom")
	m.Run = func(_ context.Context, _ Request, emit func(string)) (string, error) {
		emit("still quiet")
		return "partial", boom
	}
	ch, err = m.Start(context.Background(), Request{Name: "child", Report: "never"})
	if err != nil {
		t.Fatal(err)
	}
	got = drain(t, ch)
	if len(got) != 1 || got[0].Text != "partial" || got[0].Err != boom || !got[0].Final || got[0].Name != "child" {
		t.Fatalf("%#v", got)
	}
}

func TestEveryTurn(t *testing.T) {
	m := &Manager{Run: func(_ context.Context, _ Request, emit func(string)) (string, error) {
		emit("one")
		emit("two")
		return "done", nil
	}}
	ch, err := m.Start(context.Background(), Request{Name: "reviewer", Report: "every_turn"})
	if err != nil {
		t.Fatal(err)
	}
	got := drain(t, ch)
	if len(got) != 3 {
		t.Fatalf("%#v", got)
	}
	if got[0].Text != "one" || got[0].Final || got[1].Text != "two" || got[1].Final {
		t.Fatalf("intermediates %#v", got[:2])
	}
	if got[2].Text != "done" || !got[2].Final || got[2].Err != nil || got[2].Name != "reviewer" {
		t.Fatalf("final %#v", got[2])
	}
}

func TestEveryTurnDropsWhenSlow(t *testing.T) {
	emitted := make(chan struct{})
	m := &Manager{Run: func(_ context.Context, _ Request, emit func(string)) (string, error) {
		for i := 0; i < 10; i++ {
			emit(strconv.Itoa(i))
		}
		close(emitted)
		return "end", nil
	}}
	ch, err := m.Start(context.Background(), Request{Name: "c", Report: "every_turn"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-emitted:
	case <-time.After(2 * time.Second):
		t.Fatal("emit blocked the child")
	}
	got := drain(t, ch)
	if len(got) != 5 {
		t.Fatalf("%#v", got)
	}
	for i := 0; i < 4; i++ {
		if got[i].Final || got[i].Text != strconv.Itoa(i) || got[i].Name != "c" {
			t.Fatalf("intermediate %d: %#v", i, got[i])
		}
	}
	if !got[4].Final || got[4].Text != "end" || got[4].Err != nil {
		t.Fatalf("final %#v", got[4])
	}
}

func TestFinalSendUnblocksOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	filled := make(chan struct{})
	m := &Manager{Run: func(ctx context.Context, _ Request, emit func(string)) (string, error) {
		for i := 0; i < reportBuffer; i++ {
			emit(strconv.Itoa(i))
		}
		close(filled)
		<-ctx.Done()
		return "", ctx.Err()
	}}
	ch, err := m.Start(ctx, Request{Name: "c", Report: "every_turn", Depth: 1})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-filled:
	case <-time.After(2 * time.Second):
		t.Fatal("emit blocked the child")
	}
	cancel()
	drain(t, ch)
}

func TestInvalidReport(t *testing.T) {
	called := false
	m := &Manager{Run: func(context.Context, Request, func(string)) (string, error) {
		called = true
		return "", nil
	}}
	ch, err := m.Start(context.Background(), Request{Report: "often"})
	if !errors.Is(err, ErrReport) {
		t.Fatal(err)
	}
	if ch != nil {
		t.Fatal("channel returned")
	}
	if called {
		t.Fatal("runner called")
	}
}

func drain(t *testing.T, ch <-chan Report) []Report {
	t.Helper()
	var out []Report
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case rep, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, rep)
		case <-timer.C:
			t.Fatal("timed out waiting for reports")
		}
	}
}

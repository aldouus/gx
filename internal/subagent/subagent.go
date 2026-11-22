// Package subagent runs a child beside its parent and reports back on a channel.
//
// The report rules follow fx. final, the default, reports once when the work
// ends. every_turn also forwards each emit the runner makes along the way.
// never reports only when the work fails. Failures always report.
//
// Depth counts from the parent: the parent is 0, a child is 1, a grandchild
// is 2. A request at depth 2 or more cannot spawn. Nesting stops at two.
//
// Start returns at once. The child runs on its own goroutine, and the parent
// keeps working. Reports arrive on a channel buffered to 4. A slow consumer
// does not stall the child: an every_turn report that does not fit is dropped.
// The final report is not dropped that way. Its send waits until the consumer
// takes it or the parent context ends, so a cancellation cannot leave the
// child blocked.
package subagent

import (
	"context"
	"errors"
	"sync"
)

// reportBuffer is how many reports wait for a slow consumer.
const reportBuffer = 4

// ErrDepth is a spawn refused because Depth is already 2 or more.
var ErrDepth = errors.New("nesting stops at depth 2")

// ErrReport is a report rule other than final, every_turn, never, or empty.
var ErrReport = errors.New(`report must be "final", "every_turn", or "never"`)

// ErrRunner is a Manager with no Run function.
var ErrRunner = errors.New("nil runner")

// Request is one piece of work handed to a child.
// Depth is the caller's depth. Report empty means final.
type Request struct {
	Name, Message, Instructions, Report string
	Depth                               int
}

// Report is one message from a child. Final is the report sent when Run
// returns. Err is set when that work failed.
type Report struct {
	Name, Text string
	Final      bool
	Err        error
}

// Runner does the child's work. emit asks for an every_turn report; other
// rules ignore it. A cancelled ctx is the parent cancelling the child.
type Runner func(ctx context.Context, req Request, emit func(text string)) (string, error)

// Manager starts children with Run.
type Manager struct {
	Run Runner
}

type rule uint8

const (
	ruleFinal rule = iota
	ruleEveryTurn
	ruleNever
)

func parseRule(s string) (rule, error) {
	switch s {
	case "", "final":
		return ruleFinal, nil
	case "every_turn":
		return ruleEveryTurn, nil
	case "never":
		return ruleNever, nil
	default:
		return 0, ErrReport
	}
}

// Start runs req in the background. The channel is buffered and closed
// after the last report the rule owes. It returns ErrDepth when req.Depth
// is 2 or more, and ErrReport when req.Report is not a known rule.
func (m *Manager) Start(ctx context.Context, req Request) (<-chan Report, error) {
	if req.Depth >= 2 {
		return nil, ErrDepth
	}
	parsed, err := parseRule(req.Report)
	if err != nil {
		return nil, err
	}
	if m == nil || m.Run == nil {
		return nil, ErrRunner
	}
	run := m.Run
	ch := make(chan Report, reportBuffer)
	go runChild(ctx, req, parsed, run, ch)
	return ch, nil
}

func runChild(ctx context.Context, req Request, parsed rule, run Runner, ch chan Report) {
	defer close(ch)
	child, cancel := context.WithCancel(ctx)
	defer cancel()

	var mu sync.Mutex
	done := false
	emit := func(text string) {
		if parsed != ruleEveryTurn {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if done {
			return
		}
		select {
		case ch <- Report{Name: req.Name, Text: text}:
		default:
		}
	}

	text, runErr := run(child, req, emit)

	mu.Lock()
	done = true
	mu.Unlock()

	if parsed == ruleNever && runErr == nil {
		return
	}
	sendFinal(ctx, ch, Report{
		Name:  req.Name,
		Text:  text,
		Final: true,
		Err:   runErr,
	})
}

// sendFinal waits while ctx is active so a full buffer cannot drop the
// last report. When ctx ends it tries once more without blocking, then
// returns, so the child does not stay stuck after cancellation.
func sendFinal(ctx context.Context, ch chan<- Report, rep Report) {
	select {
	case ch <- rep:
		return
	default:
	}
	select {
	case ch <- rep:
		return
	case <-ctx.Done():
	}
	select {
	case ch <- rep:
	default:
	}
}

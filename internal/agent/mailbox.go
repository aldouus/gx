package agent

import (
	"context"
	"sync"
)

// AgentStatus is one child the parent has started.
type AgentStatus struct {
	Name string
	Done bool
}

// Mailbox holds subagent reports until the parent asks for them.
// The child pushes from its own goroutine. The parent takes them on its turn.
type Mailbox struct {
	mu    sync.Mutex
	cond  *sync.Cond
	once  sync.Once
	notes []string
	kids  []AgentStatus
}

func (m *Mailbox) init() {
	if m == nil {
		return
	}
	m.once.Do(func() { m.cond = sync.NewCond(&m.mu) })
}

// Start records a child that is still working.
func (m *Mailbox) Start(name string) {
	if m == nil || name == "" {
		return
	}
	m.init()
	m.mu.Lock()
	m.kids = append(m.kids, AgentStatus{Name: name})
	m.mu.Unlock()
}

// Finish records the child's report and marks it done.
func (m *Mailbox) Finish(name, text string) {
	if m == nil {
		return
	}
	m.init()
	m.mu.Lock()
	for i := range m.kids {
		if m.kids[i].Name == name {
			m.kids[i].Done = true
		}
	}
	m.notes = append(m.notes, "subagent "+name+" finished:\n"+text)
	m.cond.Broadcast()
	m.mu.Unlock()
}

// Pending is how many children have not finished.
func (m *Mailbox) Pending() int {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, k := range m.kids {
		if !k.Done {
			n++
		}
	}
	return n
}

// Wait blocks until every started child has finished or ctx ends.
func (m *Mailbox) Wait(ctx context.Context) error {
	if m == nil || m.Pending() == 0 {
		return nil
	}
	m.init()
	done := make(chan struct{})
	go func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		for {
			n := 0
			for _, k := range m.kids {
				if !k.Done {
					n++
				}
			}
			if n == 0 {
				close(done)
				return
			}
			m.cond.Wait()
		}
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

// Take returns reports queued since the last take.
func (m *Mailbox) Take() []string {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.notes
	m.notes = nil
	return out
}

// Agents lists children in the order they started.
func (m *Mailbox) Agents() []AgentStatus {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]AgentStatus, len(m.kids))
	copy(out, m.kids)
	return out
}

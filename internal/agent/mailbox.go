package agent

import (
	"context"
	"strings"
	"sync"
)

// AgentStatus is one child the parent has started.
type AgentStatus struct {
	Name string
	Done bool
	Text string
}

// Mailbox holds subagent reports until the parent asks for them.
// The child pushes from its own goroutine. The parent takes them on its turn.
type Mailbox struct {
	mu    sync.Mutex
	cond  *sync.Cond
	once  sync.Once
	notes []string
	kids  []child
}

type child struct {
	AgentStatus
	cancel func()
}

func (m *Mailbox) init() {
	if m == nil {
		return
	}
	m.once.Do(func() { m.cond = sync.NewCond(&m.mu) })
}

// Start records a child that is still working. cancel ends that child's turn.
func (m *Mailbox) Start(name string, cancel func()) {
	if m == nil || name == "" {
		return
	}
	m.init()
	m.mu.Lock()
	m.kids = append(m.kids, child{AgentStatus: AgentStatus{Name: name}, cancel: cancel})
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
			m.kids[i].Text = text
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
	for i := range m.kids {
		out[i] = m.kids[i].AgentStatus
	}
	return out
}

// Status describes one child, or every child when name is empty.
func (m *Mailbox) Status(name string) string {
	if m == nil {
		return "no subagents"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if name != "" {
		k, ok := m.find(name)
		if !ok {
			return "no subagent " + name
		}
		return statusLine(k)
	}
	if len(m.kids) == 0 {
		return "no subagents"
	}
	var b strings.Builder
	for i := range m.kids {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(statusLine(m.kids[i]))
	}
	return b.String()
}

// Stop cancels a running child. A finished child stays finished.
func (m *Mailbox) Stop(name string) string {
	if m == nil {
		return "no subagents"
	}
	m.init()
	m.mu.Lock()
	k, ok := m.find(name)
	if !ok {
		m.mu.Unlock()
		return "no subagent " + name
	}
	if k.Done {
		m.mu.Unlock()
		return "subagent " + name + " already finished"
	}
	cancel := k.cancel
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return "stopped " + name
}

// Close stops a child and drops it from the list.
func (m *Mailbox) Close(name string) string {
	msg := m.Stop(name)
	if m == nil {
		return msg
	}
	m.mu.Lock()
	kept := m.kids[:0]
	for _, k := range m.kids {
		if k.Name != name {
			kept = append(kept, k)
		}
	}
	m.kids = kept
	m.mu.Unlock()
	if strings.HasPrefix(msg, "stopped ") {
		return "closed " + name
	}
	return msg
}

// WaitReport waits until name finishes, or until any report arrives when
// name is empty. A report already waiting is returned at once.
func (m *Mailbox) WaitReport(ctx context.Context, name string) (string, error) {
	if m == nil {
		return "no subagents", nil
	}
	m.init()
	m.mu.Lock()
	defer m.mu.Unlock()
	for {
		if text, ok, err := m.reportLocked(name); ok {
			return text, err
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		woken := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				m.mu.Lock()
				m.cond.Broadcast()
				m.mu.Unlock()
			case <-woken:
			}
		}()
		m.cond.Wait()
		close(woken)
	}
}

func (m *Mailbox) find(name string) (child, bool) {
	for _, k := range m.kids {
		if k.Name == name {
			return k, true
		}
	}
	return child{}, false
}

func (m *Mailbox) reportLocked(name string) (string, bool, error) {
	if name != "" {
		k, ok := m.find(name)
		if !ok {
			return "no subagent " + name, true, nil
		}
		if k.Done {
			return k.Text, true, nil
		}
		return "", false, nil
	}
	if len(m.notes) > 0 {
		text := strings.Join(m.notes, "\n")
		m.notes = nil
		return text, true, nil
	}
	pending := 0
	for _, k := range m.kids {
		if !k.Done {
			pending++
		}
	}
	if pending == 0 {
		return "no subagents running", true, nil
	}
	return "", false, nil
}

func statusLine(k child) string {
	state := "running"
	if k.Done {
		state = "done"
	}
	line := k.Name + "  " + state
	if k.Text != "" {
		line += "  " + oneLine(k.Text)
	}
	return line
}

func oneLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

package agent

import "sync"

// Grant is one allow-always choice. Target is the command, path, or URL,
// or "*" when the call has none of those.
type Grant struct {
	Tool   string
	Target string
}

// Grants remembers allow-always choices for one process.
type Grants struct {
	mu    sync.Mutex
	items []Grant
}

// Allows reports whether this tool and target were allowed for the session.
// A grant whose target is "*" covers every target of that tool.
func (g *Grants) Allows(tool, target string) bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, item := range g.items {
		if item.Tool == tool && (item.Target == target || item.Target == "*") {
			return true
		}
	}
	return false
}

// Add remembers a grant. A repeat of the same pair is kept once.
func (g *Grants) Add(tool, target string) {
	if g == nil || tool == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, item := range g.items {
		if item.Tool == tool && item.Target == target {
			return
		}
	}
	g.items = append(g.items, Grant{Tool: tool, Target: target})
}

// List returns the grants in the order they were added.
func (g *Grants) List() []Grant {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]Grant, len(g.items))
	copy(out, g.items)
	return out
}

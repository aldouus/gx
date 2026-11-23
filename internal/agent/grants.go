package agent

import "sync"

// Grant is one allow-always choice. Target is the command, path, or URL,
// or "*" when the call has none of those.
type Grant struct {
	Tool   string `json:"tool"`
	Target string `json:"target"`
}

// Grants remembers allow-always choices for one process and, when bound,
// the workspace and global files.
type Grants struct {
	mu        sync.Mutex
	items     []Grant
	home      string
	workspace string
}

// Bind records where saved grants live. Empty values skip that file.
func (g *Grants) Bind(home, workspace string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.home = home
	g.workspace = workspace
	g.mu.Unlock()
}

// Allows reports whether this tool and target were allowed.
// A grant whose target is "*" covers every target of that tool.
// Saved workspace and global grants count as well as this process.
func (g *Grants) Allows(tool, target string) bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	items := g.items
	home, workspace := g.home, g.workspace
	g.mu.Unlock()
	if grantMatch(items, tool, target) {
		return true
	}
	return FilesAllow(home, workspace, tool, target)
}

// Remember stores a grant for this process and in the workspace file when
// one is bound.
func (g *Grants) Remember(tool, target string) error {
	g.Add(tool, target)
	if g == nil || tool == "" || target == "" {
		return nil
	}
	g.mu.Lock()
	workspace := g.workspace
	home := g.home
	g.mu.Unlock()
	if workspace == "" {
		return nil
	}
	return AddGrant(ScopeWorkspace, home, workspace, tool, target)
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

// Drop forgets one in-memory grant. The file is unchanged.
func (g *Grants) Drop(tool, target string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	kept := g.items[:0]
	for _, item := range g.items {
		if item.Tool == tool && item.Target == target {
			continue
		}
		kept = append(kept, item)
	}
	g.items = kept
}

// Reset forgets every in-memory grant. The files are unchanged.
func (g *Grants) Reset() {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.items = nil
	g.mu.Unlock()
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

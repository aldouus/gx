package tool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ErrOutside is a write that leaves the workspace.
var ErrOutside = errors.New("path is outside the workspace")

// Tool is one capability the model can call.
type Tool interface {
	Name() string
	Description() string
	Schema() json.RawMessage
	ReadOnly() bool
	NeedsApproval() bool
	Run(ctx context.Context, args json.RawMessage) (string, error)
}

// Registry is the tool set for one turn.
type Registry struct {
	tools []Tool
	by    map[string]Tool
}

func NewRegistry(tools ...Tool) *Registry {
	r := &Registry{by: map[string]Tool{}}
	for _, t := range tools {
		r.Add(t)
	}
	return r
}

func (r *Registry) Add(t Tool) {
	if r.by == nil {
		r.by = map[string]Tool{}
	}
	if _, ok := r.by[t.Name()]; ok {
		return
	}
	r.by[t.Name()] = t
	r.tools = append(r.tools, t)
}

func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.by[name]
	return t, ok
}

func (r *Registry) All() []Tool {
	out := make([]Tool, len(r.tools))
	copy(out, r.tools)
	return out
}

// Workspace is the set of roots a write must stay inside.
type Workspace struct {
	Roots []string
}

func (w Workspace) resolve(path string) (string, error) {
	if path == "" {
		return "", errors.New("path is empty")
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, path[2:])
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved := abs
	if eval, err := filepath.EvalSymlinks(abs); err == nil {
		resolved = eval
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	} else {
		parent := filepath.Dir(abs)
		name := filepath.Base(abs)
		if p, err := filepath.EvalSymlinks(parent); err == nil {
			resolved = filepath.Join(p, name)
		}
	}
	return resolved, nil
}

func (w Workspace) inside(resolved string) bool {
	for _, root := range w.Roots {
		base, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		if eval, err := filepath.EvalSymlinks(base); err == nil {
			base = eval
		}
		rel, err := filepath.Rel(base, resolved)
		if err != nil {
			continue
		}
		if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			return true
		}
	}
	return false
}

// ReadPath resolves a path for a read. Absolute paths outside the workspace
// are allowed. Relative paths are inside the first root.
func (w Workspace) ReadPath(path string) (string, error) {
	if !filepath.IsAbs(path) && !strings.HasPrefix(path, "~/") {
		if len(w.Roots) == 0 {
			return "", errors.New("no workspace")
		}
		path = filepath.Join(w.Roots[0], path)
	}
	return w.resolve(path)
}

// WritePath resolves a path that must stay inside a workspace root.
func (w Workspace) WritePath(path string) (string, error) {
	if !filepath.IsAbs(path) && !strings.HasPrefix(path, "~/") {
		if len(w.Roots) == 0 {
			return "", errors.New("no workspace")
		}
		path = filepath.Join(w.Roots[0], path)
	}
	resolved, err := w.resolve(path)
	if err != nil {
		return "", err
	}
	if !w.inside(resolved) {
		return "", ErrOutside
	}
	return resolved, nil
}

type progressKey struct{}

// WithProgress attaches a note callback for long tool calls.
func WithProgress(ctx context.Context, fn func(string)) context.Context {
	return context.WithValue(ctx, progressKey{}, fn)
}

// Progress emits a note when a caller installed one.
func Progress(ctx context.Context, note string) {
	fn, _ := ctx.Value(progressKey{}).(func(string))
	if fn != nil {
		fn(note)
	}
}

type depthKey struct{}

// WithDepth records how deep the running agent is. The parent is 0.
func WithDepth(ctx context.Context, depth int) context.Context {
	return context.WithValue(ctx, depthKey{}, depth)
}

// Depth is the agent depth on ctx, or 0.
func Depth(ctx context.Context) int {
	n, _ := ctx.Value(depthKey{}).(int)
	return n
}

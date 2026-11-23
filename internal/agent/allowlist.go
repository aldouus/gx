package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// Allowlist scopes. Effective is workspace plus global.
const (
	ScopeWorkspace = "workspace"
	ScopeGlobal    = "global"
	ScopeEffective = "effective"
)

var allowMu sync.Mutex

type allowFile struct {
	Grants []Grant `json:"grants"`
}

// WorkspaceAllowlist is <workspace>/.gx/allowlist.json.
func WorkspaceAllowlist(workspace string) string {
	if workspace == "" {
		return ""
	}
	return filepath.Join(workspace, ".gx", "allowlist.json")
}

// GlobalAllowlist is <home>/allowlist.json. home is GX_HOME or ~/.gx.
func GlobalAllowlist(home string) string {
	if home == "" {
		return ""
	}
	return filepath.Join(home, "allowlist.json")
}

func allowlistPath(scope, home, workspace string) (string, error) {
	switch scope {
	case ScopeWorkspace, "":
		path := WorkspaceAllowlist(workspace)
		if path == "" {
			return "", errors.New("no workspace for the allowlist")
		}
		return path, nil
	case ScopeGlobal:
		path := GlobalAllowlist(home)
		if path == "" {
			return "", errors.New("no home for the allowlist")
		}
		return path, nil
	default:
		return "", errors.New("allowlist scope is workspace or global")
	}
}

// AddGrant stores one grant. The file is mode 0600.
func AddGrant(scope, home, workspace, tool, target string) error {
	if tool == "" || target == "" {
		return errors.New("a grant needs a tool and a target")
	}
	path, err := allowlistPath(scope, home, workspace)
	if err != nil {
		return err
	}
	return mutateGrants(path, func(items *[]Grant) {
		for _, item := range *items {
			if item.Tool == tool && item.Target == target {
				return
			}
		}
		*items = append(*items, Grant{Tool: tool, Target: target})
	})
}

// RemoveGrant drops one grant. A missing pair is an error.
func RemoveGrant(scope, home, workspace, tool, target string) error {
	path, err := allowlistPath(scope, home, workspace)
	if err != nil {
		return err
	}
	found := false
	err = mutateGrants(path, func(items *[]Grant) {
		kept := (*items)[:0]
		for _, item := range *items {
			if item.Tool == tool && item.Target == target {
				found = true
				continue
			}
			kept = append(kept, item)
		}
		*items = kept
	})
	if err != nil {
		return err
	}
	if !found {
		return errors.New("no such grant")
	}
	return nil
}

// ResetGrants clears one scope.
func ResetGrants(scope, home, workspace string) error {
	path, err := allowlistPath(scope, home, workspace)
	if err != nil {
		return err
	}
	return mutateGrants(path, func(items *[]Grant) {
		*items = nil
	})
}

// ListGrants reads one scope, or both when scope is effective.
// The scope field of each returned grant is set.
func ListGrants(scope, home, workspace string) ([]ScopedGrant, error) {
	if scope == "" {
		scope = ScopeEffective
	}
	switch scope {
	case ScopeEffective:
		ws, err := readScope(ScopeWorkspace, home, workspace)
		if err != nil {
			return nil, err
		}
		gl, err := readScope(ScopeGlobal, home, workspace)
		if err != nil {
			return nil, err
		}
		return append(ws, gl...), nil
	case ScopeWorkspace, ScopeGlobal:
		return readScope(scope, home, workspace)
	default:
		return nil, errors.New("allowlist view is effective, workspace, or global")
	}
}

// ScopedGrant is a grant plus the file it came from.
type ScopedGrant struct {
	Scope string
	Grant
}

func readScope(scope, home, workspace string) ([]ScopedGrant, error) {
	path, err := allowlistPath(scope, home, workspace)
	if err != nil {
		if scope == ScopeWorkspace && workspace == "" || scope == ScopeGlobal && home == "" {
			return nil, nil
		}
		return nil, err
	}
	items, err := loadGrants(path)
	if err != nil {
		return nil, err
	}
	out := make([]ScopedGrant, len(items))
	for i, item := range items {
		out[i] = ScopedGrant{Scope: scope, Grant: item}
	}
	return out, nil
}

// FilesAllow reports whether a saved workspace or global grant covers the call.
func FilesAllow(home, workspace, tool, target string) bool {
	for _, path := range []string{WorkspaceAllowlist(workspace), GlobalAllowlist(home)} {
		if path == "" {
			continue
		}
		items, err := loadGrants(path)
		if err != nil {
			continue
		}
		if grantMatch(items, tool, target) {
			return true
		}
	}
	return false
}

func grantMatch(items []Grant, tool, target string) bool {
	for _, item := range items {
		if item.Tool == tool && (item.Target == target || item.Target == "*") {
			return true
		}
	}
	return false
}

func mutateGrants(path string, fn func(*[]Grant)) error {
	allowMu.Lock()
	defer allowMu.Unlock()
	items, err := loadGrantsUnlocked(path)
	if err != nil {
		return err
	}
	fn(&items)
	return saveGrantsUnlocked(path, items)
}

func loadGrants(path string) ([]Grant, error) {
	allowMu.Lock()
	defer allowMu.Unlock()
	return loadGrantsUnlocked(path)
}

func loadGrantsUnlocked(path string) ([]Grant, error) {
	if path == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(bytesTrimSpace(b)) == 0 {
		return nil, nil
	}
	var f allowFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	return f.Grants, nil
}

func saveGrantsUnlocked(path string, items []Grant) error {
	if path == "" {
		return errors.New("no allowlist path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(allowFile{Grants: items}, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func bytesTrimSpace(b []byte) []byte {
	i := 0
	j := len(b)
	for i < j && (b[i] == ' ' || b[i] == '\n' || b[i] == '\t' || b[i] == '\r') {
		i++
	}
	for j > i && (b[j-1] == ' ' || b[j-1] == '\n' || b[j-1] == '\t' || b[j-1] == '\r') {
		j--
	}
	return b[i:j]
}

package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type grepFiles struct{ ws Workspace }

func GrepFiles(ws Workspace) Tool { return grepFiles{ws: ws} }

func (grepFiles) Name() string { return "grep_files" }
func (grepFiles) Description() string {
	return "Search file contents for literal text (not a regex)."
}
func (grepFiles) ReadOnly() bool      { return true }
func (grepFiles) NeedsApproval() bool { return false }
func (grepFiles) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string","description":"Literal text to find."},"path":{"type":"string"},"include":{"type":"string"},"case_insensitive":{"type":"boolean"},"mode":{"type":"string","enum":["matches","files_with_matches","count"]},"context_lines":{"type":"integer"},"head_limit":{"type":"integer"},"offset":{"type":"integer"}},"required":["pattern"],"additionalProperties":false}`)
}

func (t grepFiles) Run(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Pattern         string `json:"pattern"`
		Path            string `json:"path"`
		Include         string `json:"include"`
		CaseInsensitive bool   `json:"case_insensitive"`
		Mode            string `json:"mode"`
		Context         int    `json:"context_lines"`
		Head            int    `json:"head_limit"`
		Offset          int    `json:"offset"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	if in.Pattern == "" {
		return "", fmt.Errorf("grep_files pattern is required")
	}
	if in.Mode == "" {
		in.Mode = "matches"
	}
	if in.Head == 0 {
		in.Head = 100
	}
	root := ""
	if in.Path == "" {
		if len(t.ws.Roots) == 0 {
			return "", fmt.Errorf("no workspace")
		}
		root = t.ws.Roots[0]
	} else {
		var err error
		root, err = t.ws.ReadPath(in.Path)
		if err != nil {
			return "", err
		}
	}
	needle := in.Pattern
	if in.CaseInsensitive {
		needle = strings.ToLower(needle)
	}
	var hits []string
	skipped := 0
	ig := newIgnorer()
	ig.loadRoot(root)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d == nil {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		if d.IsDir() {
			if skipSearchDir(ig, rel, d.Name()) {
				return filepath.SkipDir
			}
			ig.observe(path, rel)
			return nil
		}
		if ig.ignored(rel, false) {
			return nil
		}
		if in.Include != "" {
			ok, _ := filepath.Match(in.Include, d.Name())
			if !ok {
				return nil
			}
		}
		body, err := os.ReadFile(path)
		if err != nil || bytes.IndexByte(body, 0) >= 0 {
			return nil
		}
		lines := strings.Split(string(body), "\n")
		for i, line := range lines {
			hay := line
			if in.CaseInsensitive {
				hay = strings.ToLower(hay)
			}
			if !strings.Contains(hay, needle) {
				continue
			}
			if skipped < in.Offset {
				skipped++
				continue
			}
			switch in.Mode {
			case "files_with_matches":
				hits = append(hits, path)
				return nil
			case "count":
				hits = append(hits, path)
			default:
				hits = append(hits, formatContext(path, lines, i, in.Context))
			}
			if len(hits) >= in.Head {
				return fs.SkipAll
			}
		}
		return nil
	})
	if err != nil && err != fs.SkipAll {
		return "", err
	}
	if in.Mode == "count" {
		return fmt.Sprintf("%d\n", len(hits)), nil
	}
	if len(hits) == 0 {
		return "no matches", nil
	}
	return strings.Join(hits, "\n"), nil
}

func formatContext(path string, lines []string, i, ctx int) string {
	if ctx <= 0 {
		return fmt.Sprintf("%s:%d:%s", path, i+1, lines[i])
	}
	var b strings.Builder
	start := i - ctx
	if start < 0 {
		start = 0
	}
	end := i + ctx
	if end >= len(lines) {
		end = len(lines) - 1
	}
	for n := start; n <= end; n++ {
		mark := "-"
		if n == i {
			mark = ":"
		}
		fmt.Fprintf(&b, "%s:%d%s%s\n", path, n+1, mark, lines[n])
	}
	return strings.TrimRight(b.String(), "\n")
}

type globFiles struct{ ws Workspace }

func GlobFiles(ws Workspace) Tool { return globFiles{ws: ws} }

func (globFiles) Name() string { return "glob_files" }
func (globFiles) Description() string {
	return "Find files whose paths match a glob pattern. mode=count returns only the number of matches."
}
func (globFiles) ReadOnly() bool      { return true }
func (globFiles) NeedsApproval() bool { return false }
func (globFiles) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string"},"path":{"type":"string"},"mode":{"type":"string","enum":["matches","count"]}},"required":["pattern"],"additionalProperties":false}`)
}

func (t globFiles) Run(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
		Mode    string `json:"mode"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	root := ""
	if in.Path == "" {
		if len(t.ws.Roots) == 0 {
			return "", fmt.Errorf("no workspace")
		}
		root = t.ws.Roots[0]
	} else {
		var err error
		root, err = t.ws.ReadPath(in.Path)
		if err != nil {
			return "", err
		}
	}
	var matches []string
	ig := newIgnorer()
	ig.loadRoot(root)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d == nil {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		if d.IsDir() {
			if skipSearchDir(ig, rel, d.Name()) {
				return filepath.SkipDir
			}
			ig.observe(path, rel)
			return nil
		}
		if ig.ignored(rel, false) {
			return nil
		}
		if matchGlob(in.Pattern, rel) {
			matches = append(matches, path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if in.Mode == "count" {
		return fmt.Sprintf("%d\n", len(matches)), nil
	}
	if len(matches) == 0 {
		return "no matches", nil
	}
	return strings.Join(matches, "\n"), nil
}

func matchGlob(pattern, rel string) bool {
	rel = filepath.ToSlash(rel)
	pattern = filepath.ToSlash(pattern)
	if strings.HasPrefix(pattern, "**/") {
		rest := strings.TrimPrefix(pattern, "**/")
		if ok, _ := filepath.Match(rest, filepath.Base(rel)); ok {
			return true
		}
		parts := strings.Split(rel, "/")
		for i := range parts {
			if ok, _ := filepath.Match(rest, strings.Join(parts[i:], "/")); ok {
				return true
			}
		}
		return false
	}
	ok, _ := filepath.Match(pattern, rel)
	if ok {
		return true
	}
	ok, _ = filepath.Match(pattern, filepath.Base(rel))
	return ok
}

func skipSearchDir(ig *ignorer, rel, name string) bool {
	if rel != "." && (name == ".git" || name == "node_modules" || name == "vendor") {
		return true
	}
	return ig.ignored(rel, true)
}

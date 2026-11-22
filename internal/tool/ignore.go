package tool

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ignorer applies .gitignore files from the walk root downward.
// The last matching pattern wins. A directory pattern skips that directory.
type ignorer struct {
	patterns []ignorePattern
	seen     map[string]bool
}

type ignorePattern struct {
	neg     bool
	dirOnly bool
	base    string
	re      *regexp.Regexp
}

func newIgnorer() *ignorer {
	return &ignorer{seen: map[string]bool{}}
}

// observe loads dir's .gitignore. rel is dir relative to the walk root,
// or "" for the root itself. A missing file is not an error.
func (ig *ignorer) observe(dir, rel string) {
	rel = filepath.ToSlash(rel)
	if rel == "." {
		rel = ""
	}
	if ig.seen[rel] {
		return
	}
	ig.seen[rel] = true
	f, err := os.Open(filepath.Join(dir, ".gitignore"))
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if p, ok := parseIgnore(sc.Text(), rel); ok {
			ig.patterns = append(ig.patterns, p)
		}
	}
}

// ignored reports whether rel, relative to the walk root, is ignored.
func (ig *ignorer) ignored(rel string, isDir bool) bool {
	rel = filepath.ToSlash(rel)
	if rel == "" || rel == "." {
		return false
	}
	ignored := false
	for _, p := range ig.patterns {
		if p.matches(rel, isDir) {
			ignored = !p.neg
		}
	}
	return ignored
}

func parseIgnore(line, base string) (ignorePattern, bool) {
	line = strings.TrimRight(line, "\r")
	if line == "" || strings.HasPrefix(line, "#") {
		return ignorePattern{}, false
	}
	line = strings.TrimSpace(line)
	neg := false
	if strings.HasPrefix(line, "!") {
		neg = true
		line = strings.TrimSpace(line[1:])
	}
	if line == "" {
		return ignorePattern{}, false
	}
	dirOnly := strings.HasSuffix(line, "/")
	line = strings.TrimSuffix(line, "/")
	anchored := strings.HasPrefix(line, "/") || strings.Contains(line, "/")
	line = strings.TrimPrefix(line, "/")
	re, err := regexp.Compile(ignoreRegex(line, !anchored))
	if err != nil {
		return ignorePattern{}, false
	}
	return ignorePattern{neg: neg, dirOnly: dirOnly, base: base, re: re}, true
}

func ignoreRegex(pattern string, anywhere bool) string {
	var b strings.Builder
	b.WriteString("^")
	if anywhere {
		b.WriteString("(?:.*/)?")
	}
	for i := 0; i < len(pattern); {
		if strings.HasPrefix(pattern[i:], "**/") {
			b.WriteString("(?:.*/)?")
			i += 3
			continue
		}
		if strings.HasPrefix(pattern[i:], "**") {
			b.WriteString(".*")
			i += 2
			continue
		}
		switch pattern[i] {
		case '*':
			b.WriteString("[^/]*")
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(pattern[i : i+1]))
		}
		i++
	}
	b.WriteString("$")
	return b.String()
}

func (p ignorePattern) matches(rel string, isDir bool) bool {
	if p.dirOnly && !isDir {
		return false
	}
	if p.base != "" {
		prefix := p.base + "/"
		if rel != p.base && !strings.HasPrefix(rel, prefix) {
			return false
		}
		if rel == p.base {
			return false
		}
		rel = strings.TrimPrefix(rel, prefix)
	}
	return p.re.MatchString(rel)
}

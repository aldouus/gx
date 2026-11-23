// Package skills discovers SKILL.md directories and loads their instructions.
package skills

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	sourceHome            = "home/skills"
	sourceFX              = "~/.fx/skills"
	sourceWorkspaceGX     = "workspace/.gx/skills"
	sourceWorkspaceFX     = "workspace/.fx/skills"
	sourceWorkspaceClaude = "workspace/.claude/skills"
	sourceWorkspaceAgents = "workspace/.agents/skills"

	skillFileName = "SKILL.md"
)

// Skill is a discovered skill. Path is the SKILL.md file. Source is the root
// it came from. Discover does not include the instruction body; use Load.
type Skill struct {
	Name, Description, Path, Source, Location string
}

type root struct {
	path   string
	source string
}

var (
	errSymlinkEscape = errors.New("symlink escapes skill directory")
	errNotFile       = errors.New("SKILL.md is not a regular file")
)

// Discover finds skills under home and workspace. Earlier roots win when names
// collide. home is this port's home directory (~/.gx); skills are read from
// home/skills, then from ~/.fx/skills beside that home. Missing or unreadable
// roots are skipped.
func Discover(workspace, home string) ([]Skill, error) {
	seen := map[string]struct{}{}
	seenRoot := map[string]struct{}{}
	var out []Skill
	for _, r := range roots(workspace, home) {
		cleaned := filepath.Clean(r.path)
		if _, ok := seenRoot[cleaned]; ok {
			continue
		}
		seenRoot[cleaned] = struct{}{}
		found := scanRoot(r, seen)
		out = append(out, found...)
	}
	return out, nil
}

// Load returns the instruction body of a SKILL.md file. path may be the file
// Discover returned or the skill directory that contains it.
func Load(path string) (string, error) {
	data, err := readSkillFile(path)
	if err != nil {
		return "", err
	}
	_, _, body := parseSkill(string(data), "")
	return body, nil
}

func roots(workspace, home string) []root {
	var out []root
	if home != "" {
		out = append(out, root{filepath.Join(home, "skills"), sourceHome})
		if fx := fxSkills(home); fx != "" {
			out = append(out, root{fx, sourceFX})
		}
	}
	if workspace != "" {
		out = append(out,
			root{filepath.Join(workspace, ".gx", "skills"), sourceWorkspaceGX},
			root{filepath.Join(workspace, ".fx", "skills"), sourceWorkspaceFX},
			root{filepath.Join(workspace, ".claude", "skills"), sourceWorkspaceClaude},
			root{filepath.Join(workspace, ".agents", "skills"), sourceWorkspaceAgents},
		)
	}
	return out
}

// fxSkills is ~/.fx/skills next to this port's home (~/.gx). Discovery only reads it.
func fxSkills(home string) string {
	cleaned := filepath.Clean(home)
	if cleaned == "" || cleaned == "." {
		return ""
	}
	parent := filepath.Dir(cleaned)
	if parent == cleaned {
		return ""
	}
	return filepath.Join(parent, ".fx", "skills")
}

func scanRoot(r root, seen map[string]struct{}) []Skill {
	resolved, err := filepath.EvalSymlinks(r.path)
	if err != nil || filepath.Clean(resolved) == string(filepath.Separator) {
		return nil
	}
	entries, err := os.ReadDir(r.path)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)

	var out []Skill
	for _, name := range names {
		if name == "" || name == "." || name == ".." {
			continue
		}
		skillDir := filepath.Join(r.path, name)
		if !isSkillDir(skillDir) {
			continue
		}
		// Only the skill directory's own SKILL.md is read. Nested directories,
		// including symlinks to /, are not walked.
		skillPath := filepath.Join(skillDir, skillFileName)
		data, err := readSkillFile(skillPath)
		if err != nil {
			continue
		}
		skillName, description, _ := parseSkill(string(data), name)
		if skillName == "" {
			continue
		}
		if _, ok := seen[skillName]; ok {
			continue
		}
		seen[skillName] = struct{}{}
		out = append(out, Skill{
			Name:        skillName,
			Description: description,
			Path:        skillPath,
			Source:      r.source,
			Location:    "file://" + skillPath,
		})
	}
	return out
}

func isSkillDir(path string) bool {
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	if info.Mode()&os.ModeSymlink != 0 {
		info, err = os.Stat(path)
		if err != nil {
			return false
		}
	}
	return info.IsDir()
}

func readSkillFile(path string) ([]byte, error) {
	if filepath.Base(path) != skillFileName {
		path = filepath.Join(path, skillFileName)
	}
	skillDir := filepath.Dir(path)
	resolvedDir, err := filepath.EvalSymlinks(skillDir)
	if err != nil {
		return nil, err
	}
	resolvedDir = filepath.Clean(resolvedDir)
	filePath := filepath.Join(resolvedDir, skillFileName)
	info, err := os.Lstat(filePath)
	if err != nil {
		return nil, err
	}
	// The skill directory may itself be a symlink. A symlink at SKILL.md must
	// stay inside that directory, and a link out of / is never followed.
	if info.Mode()&os.ModeSymlink != 0 {
		if resolvedDir == string(filepath.Separator) {
			return nil, errSymlinkEscape
		}
		target, err := filepath.EvalSymlinks(filePath)
		if err != nil {
			return nil, err
		}
		if !withinDir(resolvedDir, target) {
			return nil, errSymlinkEscape
		}
		info, err = os.Lstat(target)
		if err != nil {
			return nil, err
		}
		filePath = target
	}
	if !info.Mode().IsRegular() {
		return nil, errNotFile
	}
	return os.ReadFile(filePath)
}

func withinDir(dir, target string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(target))
	if err != nil || rel == "." {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func parseSkill(content, fallbackName string) (name, description, body string) {
	name = fallbackName
	header, rest, ok := splitFrontmatter(content)
	if !ok {
		return name, "", content
	}
	body = rest
	sawName := false
	sawDescription := false
	for _, line := range strings.Split(header, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" || line[0] == '#' || line[0] == ' ' || line[0] == '\t' {
			continue
		}
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		value = unquote(strings.TrimSpace(value))
		switch key {
		case "name":
			if sawName {
				continue
			}
			sawName = true
			if value != "" {
				name = value
			}
		case "description":
			if sawDescription {
				continue
			}
			sawDescription = true
			description = value
		}
	}
	return name, description, body
}

func splitFrontmatter(content string) (header, body string, ok bool) {
	var rest string
	switch {
	case strings.HasPrefix(content, "---\r\n"):
		rest = content[len("---\r\n"):]
	case strings.HasPrefix(content, "---\n"):
		rest = content[len("---\n"):]
	case content == "---":
		return "", "", true
	default:
		return "", content, false
	}
	offset := 0
	for offset <= len(rest) {
		next := strings.IndexByte(rest[offset:], '\n')
		var line string
		nextOffset := len(rest)
		if next >= 0 {
			line = rest[offset : offset+next]
			nextOffset = offset + next + 1
		} else {
			line = rest[offset:]
		}
		if strings.TrimSuffix(line, "\r") == "---" {
			return rest[:offset], trimLeadingNewlines(rest[nextOffset:]), true
		}
		if next < 0 {
			break
		}
		offset = nextOffset
	}
	return "", content, false
}

func trimLeadingNewlines(s string) string {
	i := 0
	for i < len(s) && (s[i] == '\n' || s[i] == '\r') {
		i++
	}
	return s[i:]
}

func unquote(value string) string {
	if len(value) >= 2 {
		if (value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'') {
			return value[1 : len(value)-1]
		}
	}
	return value
}

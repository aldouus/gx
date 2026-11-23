package skills

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverPrecedence(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, ".gx")
	workspace := filepath.Join(base, "ws")

	dirs := []string{
		filepath.Join(home, "skills"),
		filepath.Join(base, ".fx", "skills"),
		filepath.Join(workspace, ".gx", "skills"),
		filepath.Join(workspace, ".fx", "skills"),
		filepath.Join(workspace, ".claude", "skills"),
		filepath.Join(workspace, ".agents", "skills"),
	}
	bodies := []string{"from-home", "from-fx", "from-wgx", "from-wfx", "from-claude", "from-agents"}
	sources := []string{
		"home/skills",
		"~/.fx/skills",
		"workspace/.gx/skills",
		"workspace/.fx/skills",
		"workspace/.claude/skills",
		"workspace/.agents/skills",
	}
	for i, dir := range dirs {
		writeSkill(t, dir, "alpha", skillDoc("alpha", bodies[i], "body "+bodies[i]+"\n"))
	}
	writeSkill(t, dirs[1], "beta", skillDoc("beta", "fx-only", "beta body\n"))
	writeSkill(t, dirs[2], "gamma", skillDoc("gamma", "gx-only", "gamma body\n"))
	writeSkill(t, dirs[3], "gamma", skillDoc("gamma", "later-gamma", "nope\n"))
	writeSkill(t, dirs[4], "delta", skillDoc("delta", "claude-only", "delta body\n"))
	writeSkill(t, dirs[5], "delta", skillDoc("delta", "later-delta", "nope\n"))
	writeSkill(t, dirs[5], "epsilon", skillDoc("epsilon", "agents-only", "epsilon body\n"))

	got, err := Discover(workspace, home)
	if err != nil {
		t.Fatal(err)
	}
	want := []Skill{
		{Name: "alpha", Description: "from-home", Path: filepath.Join(dirs[0], "alpha", "SKILL.md"), Source: sources[0], Location: "file://" + filepath.Join(dirs[0], "alpha", "SKILL.md")},
		{Name: "beta", Description: "fx-only", Path: filepath.Join(dirs[1], "beta", "SKILL.md"), Source: sources[1], Location: "file://" + filepath.Join(dirs[1], "beta", "SKILL.md")},
		{Name: "gamma", Description: "gx-only", Path: filepath.Join(dirs[2], "gamma", "SKILL.md"), Source: sources[2], Location: "file://" + filepath.Join(dirs[2], "gamma", "SKILL.md")},
		{Name: "delta", Description: "claude-only", Path: filepath.Join(dirs[4], "delta", "SKILL.md"), Source: sources[4], Location: "file://" + filepath.Join(dirs[4], "delta", "SKILL.md")},
		{Name: "epsilon", Description: "agents-only", Path: filepath.Join(dirs[5], "epsilon", "SKILL.md"), Source: sources[5], Location: "file://" + filepath.Join(dirs[5], "epsilon", "SKILL.md")},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d skills %#v, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("skill %d\n got %#v\nwant %#v", i, got[i], want[i])
		}
	}
	body, err := Load(got[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	if body != "body from-home\n" {
		t.Fatalf("Load(alpha) = %q", body)
	}
}

func TestFrontmatter(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, ".gx")
	workspace := filepath.Join(base, "ws")
	dir := filepath.Join(home, "skills")
	content := "---\r\n" +
		"description: \"Helps with testing\"\r\n" +
		"name: 'demo'\r\n" +
		"license: MIT\r\n" +
		"metadata:\r\n" +
		"  name: nested\r\n" +
		"  description: hidden\r\n" +
		"---\r\n" +
		"\r\n" +
		"# Demo\r\n" +
		"\r\n" +
		"Do the thing.\r\n" +
		"---\r\n" +
		"still body\r\n"
	path := writeSkill(t, dir, "folder", content)

	got, err := Discover(workspace, home)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %#v", got)
	}
	skill := got[0]
	if skill.Name != "demo" || skill.Description != "Helps with testing" || skill.Path != path || skill.Source != "home/skills" {
		t.Fatalf("got %#v", skill)
	}
	body, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	wantBody := "# Demo\r\n\r\nDo the thing.\r\n---\r\nstill body\r\n"
	if body != wantBody {
		t.Fatalf("Load() = %q, want %q", body, wantBody)
	}
	fromDir, err := Load(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if fromDir != wantBody {
		t.Fatalf("Load(dir) = %q", fromDir)
	}
}

func TestMissingName(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, ".gx")
	workspace := filepath.Join(base, "ws")
	dir := filepath.Join(workspace, ".agents", "skills")
	withDescription := writeSkill(t, dir, "fold-skill", "---\ndescription: Uses the folder name\n---\nInstructions here.\n")
	plain := writeSkill(t, dir, "plain-skill", "# Plain\n\nJust do the work.\n")
	writeSkill(t, dir, "blank-name", "---\nname:\ndescription: still described\n---\nBlank name body.\n")

	got, err := Discover(workspace, home)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Skill{}
	for _, skill := range got {
		byName[skill.Name] = skill
	}
	if len(byName) != 3 || len(got) != 3 {
		t.Fatalf("got %#v", got)
	}
	fold := byName["fold-skill"]
	if fold.Description != "Uses the folder name" || fold.Path != withDescription || fold.Source != "workspace/.agents/skills" {
		t.Fatalf("fold-skill %#v", fold)
	}
	body, err := Load(fold.Path)
	if err != nil {
		t.Fatal(err)
	}
	if body != "Instructions here.\n" {
		t.Fatalf("fold body %q", body)
	}
	plainSkill := byName["plain-skill"]
	if plainSkill.Description != "" || plainSkill.Path != plain {
		t.Fatalf("plain %#v", plainSkill)
	}
	plainBody, err := Load(plainSkill.Path)
	if err != nil {
		t.Fatal(err)
	}
	if plainBody != "# Plain\n\nJust do the work.\n" {
		t.Fatalf("plain body %q", plainBody)
	}
	blank := byName["blank-name"]
	if blank.Name != "blank-name" || blank.Description != "still described" {
		t.Fatalf("blank %#v", blank)
	}
}

func TestSymlinks(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, ".gx")
	workspace := filepath.Join(base, "ws")
	root := filepath.Join(home, "skills")

	outside := filepath.Join(base, "outside", "linked")
	writeSkill(t, filepath.Dir(outside), "linked", skillDoc("linked", "via-link", "linked body\n"))
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}

	hidden := filepath.Join(base, "hidden")
	secret := writeSkill(t, hidden, "secret", skillDoc("secret", "leaked", "secret body\n"))
	holder := filepath.Join(root, "holder")
	if err := os.MkdirAll(holder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(hidden, filepath.Join(holder, "down")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/", filepath.Join(holder, "to-root")); err != nil {
		t.Fatal(err)
	}

	badDir := filepath.Join(root, "bad")
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatal(err)
	}
	badFile := filepath.Join(badDir, "SKILL.md")
	if err := os.Symlink(secret, badFile); err != nil {
		t.Fatal(err)
	}

	aliasDir := filepath.Join(root, "alias")
	if err := os.MkdirAll(aliasDir, 0o755); err != nil {
		t.Fatal(err)
	}
	instructions := skillDoc("alias", "internal link", "alias body\n")
	if err := os.WriteFile(filepath.Join(aliasDir, "instructions.md"), []byte(instructions), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("instructions.md", filepath.Join(aliasDir, "SKILL.md")); err != nil {
		t.Fatal(err)
	}

	writeSkill(t, root, "stays", skillDoc("stays", "real", "stays body\n"))

	got, err := Discover(workspace, home)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Skill{}
	for _, skill := range got {
		byName[skill.Name] = skill
	}
	if _, ok := byName["secret"]; ok {
		t.Fatalf("nested symlink was followed: %#v", got)
	}
	if _, ok := byName["bad"]; ok {
		t.Fatalf("escaping SKILL.md was followed: %#v", got)
	}
	if len(byName) != 3 {
		t.Fatalf("got %#v", got)
	}
	linked := byName["linked"]
	if linked.Description != "via-link" || linked.Path != filepath.Join(root, "linked", "SKILL.md") || linked.Source != "home/skills" {
		t.Fatalf("linked %#v", linked)
	}
	body, err := Load(linked.Path)
	if err != nil {
		t.Fatal(err)
	}
	if body != "linked body\n" {
		t.Fatalf("linked body %q", body)
	}
	alias := byName["alias"]
	if alias.Description != "internal link" {
		t.Fatalf("alias %#v", alias)
	}
	aliasBody, err := Load(alias.Path)
	if err != nil {
		t.Fatal(err)
	}
	if aliasBody != "alias body\n" {
		t.Fatalf("alias body %q", aliasBody)
	}
	if _, err := Load(badFile); !errors.Is(err, errSymlinkEscape) {
		t.Fatalf("Load(escaped) = %v", err)
	}
}

func TestDiscoverSkipsUnreadableRoot(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, ".gx")
	workspace := filepath.Join(base, "ws")
	blocked := filepath.Join(home, "skills")
	if err := os.MkdirAll(blocked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(blocked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o755) })
	if _, err := os.ReadDir(blocked); err == nil {
		t.Skip("directory remains readable")
	}
	writeSkill(t, filepath.Join(workspace, ".gx", "skills"), "visible", skillDoc("visible", "ok", "body\n"))

	got, err := Discover(workspace, home)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "visible" || got[0].Source != "workspace/.gx/skills" {
		t.Fatalf("got %#v", got)
	}
}

func skillDoc(name, description, body string) string {
	return "---\nname: " + name + "\ndescription: " + description + "\n---\n" + body
}

func writeSkill(t *testing.T, root, name, content string) string {
	t.Helper()
	path := filepath.Join(root, name, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

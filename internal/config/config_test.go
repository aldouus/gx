package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLoad(t *testing.T) {
	home := t.TempDir()
	in := File{Settings: Settings{Model: "demo", PermissionMode: "ask"}}
	if err := Save(home, in); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(home, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
	out, err := Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if out.Model != "demo" || out.PermissionMode != "ask" {
		t.Fatalf("%+v", out)
	}
}

func TestMissingSettings(t *testing.T) {
	f, err := Load(filepath.Join(t.TempDir(), "missing"))
	if err != nil {
		t.Fatal(err)
	}
	if f.Model != "" {
		t.Fatal(f.Model)
	}
}

func TestProfileAndAgents(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".gx", "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"model":"m","permission_mode":"ask","prompt":"be brief","tools":["read_file"]}`)
	if err := os.WriteFile(filepath.Join(ws, ".gx", "agents", "review.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := LoadProfile(home, ws, "review")
	if err != nil {
		t.Fatal(err)
	}
	if p.Model != "m" || p.PermissionMode != "ask" || len(p.Tools) != 1 {
		t.Fatalf("%+v", p)
	}
	if err := os.WriteFile(filepath.Join(home, "AGENTS.md"), []byte("home rule"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "AGENTS.md"), []byte("repo rule"), 0o644); err != nil {
		t.Fatal(err)
	}
	text, err := ProjectInstructions(home, ws, ws)
	if err != nil {
		t.Fatal(err)
	}
	if text != "home rule\n\nrepo rule" {
		t.Fatalf("%q", text)
	}
}

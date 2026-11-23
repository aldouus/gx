package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSearchHonorsExcludeAndClasses(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".git/info/exclude", "excluded.txt\n")
	write(".gitignore", "file[0-9].txt\n")
	write("excluded.txt", "EXCLUDED\n")
	write("file1.txt", "CLASS\n")
	write("fileA.txt", "KEPT\n")
	got, err := GrepFiles(Workspace{Roots: []string{dir}}).Run(context.Background(), json.RawMessage(`{"pattern":"E"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "EXCLUDED") || strings.Contains(got, "CLASS") {
		t.Fatalf("exclude or class missed:\n%s", got)
	}
	if !strings.Contains(got, "KEPT") {
		t.Fatalf("kept file missing:\n%s", got)
	}
}

func TestSearchHonorsGitignore(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".gitignore", "secret.txt\nskip/\n*.log\n!keep.log\n")
	write("secret.txt", "SECRET\n")
	write("visible.txt", "VISIBLE\n")
	write("noise.log", "NOISE\n")
	write("keep.log", "KEEP\n")
	write("skip/hidden.txt", "HIDDEN\n")
	ws := Workspace{Roots: []string{dir}}
	got, err := GrepFiles(ws).Run(context.Background(), json.RawMessage(`{"pattern":"E"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "VISIBLE") || !strings.Contains(got, "KEEP") {
		t.Fatalf("missing visible hits:\n%s", got)
	}
	if strings.Contains(got, "SECRET") || strings.Contains(got, "NOISE") || strings.Contains(got, "HIDDEN") {
		t.Fatalf("ignored text was searched:\n%s", got)
	}
	files, err := GlobFiles(ws).Run(context.Background(), json.RawMessage(`{"pattern":"**/*"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(files, "secret.txt") || strings.Contains(files, "noise.log") || strings.Contains(files, "hidden.txt") {
		t.Fatalf("ignored paths listed:\n%s", files)
	}
	if !strings.Contains(files, "keep.log") || !strings.Contains(files, "visible.txt") {
		t.Fatalf("visible paths missing:\n%s", files)
	}
}

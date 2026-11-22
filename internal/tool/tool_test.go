package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadEditGrep(t *testing.T) {
	dir := t.TempDir()
	ws := Workspace{Roots: []string{dir}}
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	read, err := ReadFile(ws).Run(context.Background(), json.RawMessage(`{"path":"a.txt","start_line":2,"line_count":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read, "2\ttwo") || strings.Contains(read, "three") {
		t.Fatalf("read:\n%s", read)
	}
	if _, err := EditFile(ws).Run(context.Background(), json.RawMessage(`{"path":"a.txt","old_string":"two","new_string":"TWO"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := EditFile(ws).Run(context.Background(), json.RawMessage(`{"path":"a.txt","old_string":"two","new_string":"x"}`)); err == nil {
		t.Fatal("second edit should miss")
	}
	outside := filepath.Join(t.TempDir(), "nope.txt")
	args, _ := json.Marshal(map[string]string{"path": outside, "content": "x"})
	if _, err := WriteFile(ws).Run(context.Background(), args); err != ErrOutside {
		t.Fatalf("outside write: %v", err)
	}
	got, err := GrepFiles(ws).Run(context.Background(), json.RawMessage(`{"pattern":"TWO"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "TWO") {
		t.Fatalf("grep: %s", got)
	}
	files, err := GlobFiles(ws).Run(context.Background(), json.RawMessage(`{"pattern":"**/*.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(files, "a.txt") {
		t.Fatalf("glob: %s", files)
	}
}

func TestShellClean(t *testing.T) {
	dir := t.TempDir()
	ws := Workspace{Roots: []string{dir}}
	out, err := Shell(ws).Run(context.Background(), json.RawMessage(`{"action":"run","command":"printf hi","profile":"clean","yield_time_ms":5000}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "hi") || !strings.Contains(out, "exit_code: 0") {
		t.Fatalf("shell: %q", out)
	}
}

func TestShellCancel(t *testing.T) {
	dir := t.TempDir()
	ws := Workspace{Roots: []string{dir}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Shell(ws).Run(ctx, json.RawMessage(`{"action":"run","command":"sleep 30","profile":"clean","yield_time_ms":1000}`))
	if err == nil {
		t.Fatal("expected cancel")
	}
}

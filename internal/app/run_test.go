package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gx/internal/tui"
)

func TestHelpExit(t *testing.T) {
	var out, err bytes.Buffer
	code := Run([]string{"help"}, strings.NewReader(""), &out, &err)
	if code != 0 {
		t.Fatal(code)
	}
	if !strings.Contains(out.String(), "Usage:") || !strings.Contains(out.String(), "ask") {
		t.Fatal(out.String())
	}
}

func TestVersion(t *testing.T) {
	var out, err bytes.Buffer
	if code := Run([]string{"-v"}, nil, &out, &err); code != 0 {
		t.Fatal(code)
	}
	if strings.TrimSpace(out.String()) == "" {
		t.Fatal("empty version")
	}
}

func TestUnknownFlag(t *testing.T) {
	var out, err bytes.Buffer
	code := Run([]string{"--not-a-flag"}, nil, &out, &err)
	if code != 2 {
		t.Fatalf("code %d err %s", code, err.String())
	}
	if !strings.Contains(err.String(), "unknown flag") {
		t.Fatal(err.String())
	}
}

func TestAskMissingPrompt(t *testing.T) {
	var out, err bytes.Buffer
	code := Run([]string{"ask"}, strings.NewReader(""), &out, &err)
	if code != 2 || !strings.Contains(err.String(), "missing prompt") {
		t.Fatalf("code %d %s", code, err.String())
	}
}

func TestNoTTY(t *testing.T) {
	if tui.IsTerminal(tui.StdinFD()) && tui.IsTerminal(tui.StdoutFD()) {
		t.Skip("this process has a terminal")
	}
	var out, err bytes.Buffer
	code := Run(nil, strings.NewReader(""), &out, &err)
	if code != 1 || !strings.Contains(err.String(), "interactive terminal") {
		t.Fatalf("code %d %s", code, err.String())
	}
}

func TestStatus(t *testing.T) {
	t.Setenv("GX_HOME", t.TempDir())
	var out, err bytes.Buffer
	code := Run([]string{"status"}, nil, &out, &err)
	if code != 0 {
		t.Fatal(err.String())
	}
	if !strings.Contains(out.String(), "permission_mode=") || !strings.Contains(out.String(), "gx_home=") {
		t.Fatal(out.String())
	}
}

func TestAttachImage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dot.png")
	if err := os.WriteFile(path, []byte{0x89, 'P', 'N', 'G'}, 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	tr := tui.NewTranscript(&buf, 80)
	var images []string
	if err := attachImage(dir, "dot.png", &images, tr); err != nil {
		t.Fatal(err)
	}
	if len(images) != 1 || images[0] != path {
		t.Fatalf("%v", images)
	}
	if !strings.Contains(buf.String(), "attached") {
		t.Fatal(buf.String())
	}
	if err := listImages("clear", &images, tr); err != nil {
		t.Fatal(err)
	}
	if len(images) != 0 {
		t.Fatalf("cleared: %v", images)
	}
}

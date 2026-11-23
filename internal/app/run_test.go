package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gx/internal/agent"
	"gx/internal/auth"
	"gx/internal/cli"
	"gx/internal/config"
	"gx/internal/session"
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

func TestDoctorNoticesSubscription(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GX_HOME", home)
	t.Setenv("OPENAI_API_KEY", "")
	if err := auth.Save(home, auth.Credential{Provider: "grok", AccessToken: "tok"}); err != nil {
		t.Fatal(err)
	}
	var out, err bytes.Buffer
	if code := Run([]string{"doctor"}, nil, &out, &err); code != 0 {
		t.Fatal(err.String())
	}
	text := out.String()
	if !strings.Contains(text, "grok: signed in") || !strings.Contains(text, "codex: not signed in") {
		t.Fatal(text)
	}
}

func TestSessionResumeOpensTranscript(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GX_HOME", home)
	s, err := session.Create(home, "/work")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.User("hello resume"); err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := Run([]string{"session", "resume"}, nil, &out, &errOut); code != 0 {
		t.Fatal(errOut.String())
	}
	if !strings.Contains(out.String(), "hello resume") {
		t.Fatal(out.String())
	}
	if strings.Contains(errOut.String(), "fx -r") {
		t.Fatal(errOut.String())
	}
}

func TestSignedInCopy(t *testing.T) {
	if signedInText("codex") != "Signed in with Codex." {
		t.Fatal(signedInText("codex"))
	}
	if signedInText("grok") != "Signed in with Grok." {
		t.Fatal(signedInText("grok"))
	}
}

func TestAllowlistSlashPersists(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	var buf bytes.Buffer
	tr := tui.NewTranscript(&buf, 80)
	grants := &agent.Grants{}
	grants.Bind(home, cwd)
	line := "/allowlist global add web_fetch https://example.com"
	if err := handleSubmit(cli.Invocation{}, &config.File{}, home, cwd, tr, &buf, line, nil, nil, grants, nil); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(agent.GlobalAllowlist(home))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
	if !grants.Allows("web_fetch", "https://example.com") {
		t.Fatal("grant was not consulted")
	}
	buf.Reset()
	if err := handleSubmit(cli.Invocation{}, &config.File{}, home, cwd, tr, &buf, "/allowlist view global", nil, nil, grants, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "web_fetch") || !strings.Contains(buf.String(), "https://example.com") {
		t.Fatal(buf.String())
	}
	buf.Reset()
	if err := handleSubmit(cli.Invocation{}, &config.File{}, home, cwd, tr, &buf, "/allowlist global remove web_fetch https://example.com", nil, nil, grants, nil); err != nil {
		t.Fatal(err)
	}
	if grants.Allows("web_fetch", "https://example.com") {
		t.Fatal("removed grant still allowed")
	}
	buf.Reset()
	if err := handleSubmit(cli.Invocation{}, &config.File{}, home, cwd, tr, &buf, "/allowlist workspace add shell ls", nil, nil, grants, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(agent.WorkspaceAllowlist(cwd)); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	if err := handleSubmit(cli.Invocation{}, &config.File{}, home, cwd, tr, &buf, "/allowlist workspace reset", nil, nil, grants, nil); err != nil {
		t.Fatal(err)
	}
	list, err := agent.ListGrants(agent.ScopeWorkspace, home, cwd)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("reset %#v", list)
	}
}

func TestShellReloadSlash(t *testing.T) {
	var buf bytes.Buffer
	tr := tui.NewTranscript(&buf, 80)
	if err := handleSubmit(cli.Invocation{}, &config.File{}, t.TempDir(), t.TempDir(), tr, &buf, "/shell", nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "usage: /shell reload") {
		t.Fatal(buf.String())
	}
	buf.Reset()
	if err := handleSubmit(cli.Invocation{}, &config.File{}, t.TempDir(), t.TempDir(), tr, &buf, "/shell reload", nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "shell snapshot cleared") {
		t.Fatal(buf.String())
	}
}

func TestForkSlashContinuesInTheCopy(t *testing.T) {
	home := t.TempDir()
	s, err := session.Create(home, "/work")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.User("hello"); err != nil {
		t.Fatal(err)
	}
	if err := s.Assistant("hi"); err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}
	old := s.ID()
	want := s.TranscriptText()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := session.Remember(home, old); err != nil {
		t.Fatal(err)
	}
	continued := true
	var buf bytes.Buffer
	tr := tui.NewTranscript(&buf, 80)
	if err := handleSubmit(cli.Invocation{}, &config.File{}, home, "/work", tr, &buf, "/fork", &continued, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	got, err := session.Remembered(home)
	if err != nil {
		t.Fatal(err)
	}
	if got == "" || got == old {
		t.Fatalf("remembered %q", got)
	}
	cp, err := session.Open(home, got)
	if err != nil {
		t.Fatal(err)
	}
	defer cp.Close()
	if cp.TranscriptText() != want {
		t.Fatalf("copy:\n%s", cp.TranscriptText())
	}
	src, err := session.Open(home, old)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if src.TranscriptText() != want {
		t.Fatal("old transcript changed")
	}
	if !continued {
		t.Fatal("fork dropped the current session")
	}
}

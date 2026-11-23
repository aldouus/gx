package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCopyReplaysTranscript(t *testing.T) {
	root := t.TempDir()
	s, err := Create(root, "/work")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.User("hello"); err != nil {
		t.Fatal(err)
	}
	if err := s.Assistant("hi there"); err != nil {
		t.Fatal(err)
	}
	if err := s.ToolCall("call-1", "shell", `{"command":"pwd"}`); err != nil {
		t.Fatal(err)
	}
	if err := s.ToolResult("call-1", "shell", "/work"); err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}
	oldID := s.ID()
	want := s.TranscriptText()
	oldLog, err := os.ReadFile(filepath.Join(root, "sessions", oldID, "log.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	cp, err := s.Copy()
	if err != nil {
		t.Fatal(err)
	}
	if cp.ID() == oldID {
		t.Fatal("copy reused the source id")
	}
	if got := cp.TranscriptText(); got != want {
		t.Fatalf("copy transcript:\n%s\nwant:\n%s", got, want)
	}
	if s.ID() != oldID || s.TranscriptText() != want {
		t.Fatal("source session changed during copy")
	}
	if err := cp.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(root, "sessions", oldID, "log.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(oldLog) {
		t.Fatal("old log bytes changed")
	}
	replayed, err := Open(root, cp.ID())
	if err != nil {
		t.Fatal(err)
	}
	defer replayed.Close()
	if got := replayed.TranscriptText(); got != want {
		t.Fatalf("replayed transcript:\n%s\nwant:\n%s", got, want)
	}
	src, err := Open(root, oldID)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if src.ID() != oldID || src.TranscriptText() != want {
		t.Fatal("old id changed after reopen")
	}
}

func TestCommitSurvivesClose(t *testing.T) {
	root := t.TempDir()
	s, err := Create(root, "/work")
	if err != nil {
		t.Fatal(err)
	}
	id := s.ID()
	if _, err := s.User("hello"); err != nil {
		t.Fatal(err)
	}
	if err := s.Assistant("hi"); err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(root, id)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	text := s.TranscriptText()
	if !strings.Contains(text, "hello") || !strings.Contains(text, "hi") {
		t.Fatalf("transcript:\n%s", text)
	}
	for _, ev := range s.Events() {
		if ev.Kind == "turn_interrupted" {
			t.Fatal("committed turn was interrupted")
		}
	}
}

func TestSteerStaysOnTheOpenTurn(t *testing.T) {
	root := t.TempDir()
	s, err := Create(root, "/work")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.User("hello"); err != nil {
		t.Fatal(err)
	}
	if err := s.Assistant("working"); err != nil {
		t.Fatal(err)
	}
	if err := s.Steer("subagent child-1 finished:\ndone"); err != nil {
		t.Fatal(err)
	}
	hist := s.History()
	if len(hist) < 3 || hist[2].Role != "user" || !strings.Contains(hist[2].Text, "done") {
		t.Fatalf("history %#v", hist)
	}
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestBusySecondWriter(t *testing.T) {
	root := t.TempDir()
	s, err := Create(root, "/work")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := Open(root, s.ID()); err != ErrBusy {
		t.Fatalf("second open: %v", err)
	}
}

func TestUnfinishedTurnIsNotContinued(t *testing.T) {
	if os.Getenv("GX_SESSION_CRASH") == "1" {
		s, err := Create(os.Getenv("GX_SESSION_ROOT"), "/work")
		if err != nil {
			os.Stderr.WriteString(err.Error())
			os.Exit(2)
		}
		os.WriteFile(os.Getenv("GX_SESSION_IDFILE"), []byte(s.ID()), 0o600)
		if _, err := s.User("keep going"); err != nil {
			os.Exit(2)
		}
		if err := s.Assistant("partial"); err != nil {
			os.Exit(2)
		}
		s.Abandon()
		os.Exit(0)
	}
	root := t.TempDir()
	idFile := root + "/id"
	cmd := exec.Command(os.Args[0], "-test.run=TestUnfinishedTurnIsNotContinued", "-test.v=false")
	cmd.Env = append(os.Environ(), "GX_SESSION_CRASH=1", "GX_SESSION_ROOT="+root, "GX_SESSION_IDFILE="+idFile)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
	idBytes, err := os.ReadFile(idFile)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(root, string(idBytes))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var sawInterrupt bool
	for _, ev := range s.Events() {
		if ev.Kind == "turn_interrupted" {
			sawInterrupt = true
		}
		if ev.Kind == "turn_committed" {
			t.Fatal("unfinished turn was committed")
		}
	}
	if !sawInterrupt {
		t.Fatal("missing turn_interrupted")
	}
	text := s.TranscriptText()
	if !strings.Contains(text, "partial") || !strings.Contains(text, "[interrupted]") {
		t.Fatalf("transcript:\n%s", text)
	}
	hist := s.History()
	if len(hist) == 0 || hist[len(hist)-1].Role == "assistant" && !strings.Contains(hist[len(hist)-1].Text, "[interrupted]") {
		t.Fatalf("history %+v", hist)
	}
}

func TestTornTailIsDropped(t *testing.T) {
	root := t.TempDir()
	s, err := Create(root, "/work")
	if err != nil {
		t.Fatal(err)
	}
	id := s.ID()
	if _, err := s.User("one"); err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.file.Write([]byte(`{"v":1,"seq":99`)); err != nil {
		t.Fatal(err)
	}
	s.Abandon()
	s, err = Open(root, id)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	text := s.TranscriptText()
	if !strings.Contains(text, "one") {
		t.Fatalf("lost committed turn:\n%s", text)
	}
}

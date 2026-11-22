package tui

import (
	"bytes"
	"strings"
	"testing"
)

func TestTranscriptOrderAndDiff(t *testing.T) {
	var buf bytes.Buffer
	tr := NewTranscript(&buf, 80)
	tr.SetStatus("demo", "ask", true)
	if err := tr.User("hello"); err != nil {
		t.Fatal(err)
	}
	if err := tr.Token("ab"); err != nil {
		t.Fatal(err)
	}
	if err := tr.Token("c"); err != nil {
		t.Fatal(err)
	}
	if err := tr.FinishText(); err != nil {
		t.Fatal(err)
	}
	if err := tr.ToolStart("shell", "echo hi"); err != nil {
		t.Fatal(err)
	}
	if err := tr.Permission("shell", "echo hi"); err != nil {
		t.Fatal(err)
	}
	plain := strip(buf.String())
	iUser := strings.Index(plain, "hello")
	iAsst := strings.Index(plain, "abc")
	iTool := strings.Index(plain, "Running shell")
	iPerm := strings.Index(plain, "Allow shell?")
	if iUser < 0 || iAsst < 0 || iTool < 0 || iPerm < 0 || !(iUser < iAsst && iAsst < iTool && iTool < iPerm) {
		t.Fatalf("order user=%d asst=%d tool=%d perm=%d\n%s", iUser, iAsst, iTool, iPerm, plain)
	}
	if strings.Count(plain, "abc") != 1 {
		t.Fatalf("assistant text redrawn:\n%s", plain)
	}
	if !strings.Contains(plain, "1. Allow once") || !strings.Contains(plain, "2. Allow always") || !strings.Contains(plain, "3. Deny") {
		t.Fatal("permission prompt is missing the three choices")
	}
	before := buf.Len()
	tr.SetInput("next", 4)
	if err := tr.Paint(); err != nil {
		t.Fatal(err)
	}
	tail := strip(buf.String()[before:])
	if strings.Contains(tail, "abc") || strings.Contains(tail, "hello") {
		t.Fatalf("scrollback rewritten: %q", tail)
	}
}

func TestUnchangedFrameRowIsNotRewritten(t *testing.T) {
	var buf bytes.Buffer
	s := NewScreen(&buf)
	if err := s.Paint(Frame{Rows: []string{"divider", "input"}, CursorRow: 1, CursorCol: 1}); err != nil {
		t.Fatal(err)
	}
	before := buf.Len()
	if err := s.Paint(Frame{Rows: []string{"divider", "input2"}, CursorRow: 1, CursorCol: 1}); err != nil {
		t.Fatal(err)
	}
	tail := buf.String()[before:]
	if strings.Contains(tail, "divider") {
		t.Fatalf("unchanged row rewritten: %q", tail)
	}
	if !strings.Contains(tail, "input2") {
		t.Fatalf("changed row missing: %q", tail)
	}
}

func TestCtrlCArmsThenExits(t *testing.T) {
	var c Composer
	first := c.Feed([]byte{3})
	if first.Exit || !c.Armed {
		t.Fatalf("first ctrl+c armed=%v exit=%v", c.Armed, first.Exit)
	}
	if Hint(c.Armed) != "press ctrl+c again to exit" {
		t.Fatal(Hint(c.Armed))
	}
	second := c.Feed([]byte{3})
	if !second.Exit {
		t.Fatal("second ctrl+c should exit")
	}
}

func TestCtrlOAndSubmit(t *testing.T) {
	var c Composer
	c.Feed([]byte("hi"))
	act := c.Feed([]byte{15})
	if !act.Detail {
		t.Fatal("ctrl+o")
	}
	act = c.Feed([]byte("\r"))
	if act.Submit != "hi" {
		t.Fatalf("submit %q", act.Submit)
	}
}

func TestDetailShowsToolOutput(t *testing.T) {
	var buf bytes.Buffer
	tr := NewTranscript(&buf, 80)
	if err := tr.ToolStart("shell", "echo"); err != nil {
		t.Fatal(err)
	}
	if err := tr.ToolDone("shell", "line1\nline2"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strip(buf.String()), "line2") {
		t.Fatal("collapsed output showed line2")
	}
	tr.ToggleDetail()
	if err := tr.Paint(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strip(buf.String()), "line2") {
		t.Fatal("ctrl+o did not show the full tool output")
	}
	if !strings.Contains(buf.String(), "\x1b[?1049h") {
		t.Fatal("ctrl+o did not open the alternate screen")
	}
	tr.ToggleDetail()
	if err := tr.Paint(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "\x1b[?1049l") {
		t.Fatal("ctrl+o did not restore the primary screen")
	}
}

func strip(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			if i+1 < len(s) && s[i+1] == '[' {
				i += 2
				for i < len(s) && !(s[i] >= 0x40 && s[i] <= 0x7e) {
					i++
				}
				continue
			}
			if i+1 < len(s) {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

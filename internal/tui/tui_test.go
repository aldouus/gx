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
	iTool := strings.Index(plain, "Ran echo hi")
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

func TestComposerRepaintDoesNotStack(t *testing.T) {
	var buf bytes.Buffer
	s := NewScreen(&buf)
	if err := s.Paint(Frame{Rows: []string{"┃ h", "hint"}, CursorRow: 0, CursorCol: 4}); err != nil {
		t.Fatal(err)
	}
	before := buf.Len()
	if err := s.Paint(Frame{Rows: []string{"┃ he", "hint"}, CursorRow: 0, CursorCol: 5}); err != nil {
		t.Fatal(err)
	}
	tail := buf.String()[before:]
	if strings.Contains(tail, "\n") || strings.Contains(tail, "\x1b[1E") {
		t.Fatalf("repaint scrolled a new prompt line: %q", tail)
	}
	if !strings.Contains(tail, "┃ he") {
		t.Fatalf("new composer missing: %q", tail)
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

func TestTurnSpacingMatchesFx(t *testing.T) {
	var buf bytes.Buffer
	tr := NewTranscript(&buf, 80)
	tr.SetStatus("grok-4.7", "auto", true)
	tr.SetPlace("", "medium")
	if err := tr.User("hey"); err != nil {
		t.Fatal(err)
	}
	if err := tr.Token("\n\nHey. What are you working on?\n\n"); err != nil {
		t.Fatal(err)
	}
	if err := tr.FinishText(); err != nil {
		t.Fatal(err)
	}
	if err := tr.User("weirdo"); err != nil {
		t.Fatal(err)
	}
	plain := replay(buf.String())
	hey := strings.Index(plain, "hey")
	reply := strings.Index(plain, "Hey. What are you working on?")
	next := strings.Index(plain, "weirdo")
	if hey < 0 || reply < 0 || next < 0 {
		t.Fatal(plain)
	}
	if strings.Count(plain[hey:reply], "\n") != 2 || strings.Count(plain[reply:next], "\n") != 2 {
		t.Fatalf("gaps hey=%d reply=%d\n%s", strings.Count(plain[hey:reply], "\n"), strings.Count(plain[reply:next], "\n"), plain)
	}
	frame := tr.frame()
	var tail []string
	for _, row := range frame.Rows {
		tail = append(tail, strip(row))
	}
	if len(tail) < 4 || !strings.HasPrefix(tail[len(tail)-4], "\u2503") || tail[len(tail)-3] != "" || tail[len(tail)-2] != "auto \u00b7 grok-4.7 \u00b7 medium" || tail[len(tail)-1] != "" {
		t.Fatalf("footer %#v", tail)
	}
}

func TestFooterChrome(t *testing.T) {
	var buf bytes.Buffer
	tr := NewTranscript(&buf, 80)
	tr.SetStatus("demo", "ask", true)
	tr.SetPlace("/work/gx", "high")
	tr.SetInput("next", 4)
	if err := tr.Paint(); err != nil {
		t.Fatal(err)
	}
	plain := strip(buf.String())
	if strings.Contains(plain, "❯") {
		t.Fatal("composer still uses the old prompt")
	}
	if !strings.Contains(plain, "┃ next") {
		t.Fatalf("composer %s", plain)
	}
	if strings.Contains(plain, "─") {
		t.Fatal("idle chrome drew a rule")
	}
	for _, want := range []string{"ask", "demo", "high"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("hint missing %s\n%s", want, plain)
		}
	}
	tr.SetBusy(true)
	if err := tr.Paint(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strip(buf.String()), "working") {
		t.Fatal(strip(buf.String()))
	}
}

func TestPermissionFooterAndKeys(t *testing.T) {
	var buf bytes.Buffer
	tr := NewTranscript(&buf, 80)
	if err := tr.Permission("shell", "echo hi"); err != nil {
		t.Fatal(err)
	}
	plain := strip(buf.String())
	if !strings.Contains(plain, "Allow shell?") || !strings.Contains(plain, "1. Allow once") {
		t.Fatalf("footer %s", plain)
	}
	if strings.Contains(plain, "❯") || strings.Contains(plain, "─") {
		t.Fatal("permission chrome used the idle composer")
	}
	choice, note, err := DecidePermission(strings.NewReader("\x1b[B\x1b[B\tnope\n"), tr.SetPermissionAmend)
	if err != nil || choice != 2 || note != "nope" {
		t.Fatalf("choice %d note %q err %v", choice, note, err)
	}
	if !strings.Contains(strip(buf.String()), "No, and tell gx what to do differently") {
		t.Fatal(strip(buf.String()))
	}
	choice, note, err = DecidePermission(strings.NewReader("\tyes please\n"), nil)
	if err != nil || choice != 0 || note != "yes please" {
		t.Fatalf("amend choice %d note %q err %v", choice, note, err)
	}
	choice, _, err = DecidePermission(strings.NewReader("y"), nil)
	if err != nil || choice != 0 {
		t.Fatalf("y choice %d err %v", choice, err)
	}
	choice, note, err = DecidePermission(strings.NewReader("nay\n"), nil)
	if err != nil || choice != 2 || note != "" {
		t.Fatalf("n before typing choice %d note %q", choice, note)
	}
}

func TestProgramStatus(t *testing.T) {
	var buf bytes.Buffer
	tr := NewTranscript(&buf, 40)
	if err := tr.Paint(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "\x1b]7501;state=idle:app=gx\x1b\\") {
		t.Fatal(buf.String())
	}
	tr.SetBusy(true)
	if !strings.Contains(buf.String(), "\x1b]7501;state=working:app=gx\x1b\\") {
		t.Fatal("missing working")
	}
	if err := tr.Permission("shell", "git push"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "state=blocked:kind=permission:app=gx:msg=") {
		t.Fatal(buf.String())
	}
	tr.End()
	if !strings.Contains(buf.String(), "\x1b]7501;state=clear\x1b\\") {
		t.Fatal("missing clear")
	}
	if err := tr.ClearPermission(); err != nil {
		t.Fatal(err)
	}
	tr.SetBusy(false)
	tr.SetResult("done")
	if !strings.Contains(buf.String(), "\x1b]7501;state=done:app=gx\x1b\\") {
		t.Fatal("missing done")
	}
	tr.SetResult("error")
	if !strings.Contains(buf.String(), "\x1b]7501;state=error:app=gx\x1b\\") {
		t.Fatal("missing error")
	}
}

func TestToolTreeStacks(t *testing.T) {
	var buf bytes.Buffer
	tr := NewTranscript(&buf, 80)
	if err := tr.ToolStart("grep_files", `{"pattern":"List"}`); err != nil {
		t.Fatal(err)
	}
	if err := tr.ToolDone("grep_files", "handlers.go"); err != nil {
		t.Fatal(err)
	}
	if err := tr.ToolStart("read_file", `{"path":"handlers.go","start_line":690}`); err != nil {
		t.Fatal(err)
	}
	if err := tr.ToolDone("read_file", "package"); err != nil {
		t.Fatal(err)
	}
	if err := tr.CommitTools(); err != nil {
		t.Fatal(err)
	}
	plain := strip(buf.String())
	if !strings.Contains(plain, "Searched, Read") || !strings.Contains(plain, "Searched List") || !strings.Contains(plain, "Read handlers.go") {
		t.Fatal(plain)
	}
	if !strings.Contains(plain, "├") || !strings.Contains(plain, "└") {
		t.Fatal(plain)
	}
}

func TestSignInLinkSurvivesWrap(t *testing.T) {
	var buf bytes.Buffer
	tr := NewTranscript(&buf, 40)
	url := "https://auth.example/oauth?referrer=gx&code=abcdefghijklmnopqrstuvwxyz"
	if err := tr.ShowSignIn("Open this URL to sign in with Grok:\n" + url + "\nWaiting for browser authorization...\n"); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if strings.Count(got, "\x1b]8;;"+url+"\x1b\\") < 2 {
		t.Fatalf("wrapped url is not one link: %q", got)
	}
	if strings.Contains(got, "referrer=fx") {
		t.Fatal(got)
	}
}

func TestArrowSelectsSlashMenu(t *testing.T) {
	var c Composer
	c.Line = "/log"
	c.Pos = len(c.Line)
	act := c.Feed([]byte("\x1b[B"))
	if !act.Down {
		t.Fatal("down arrow did not move the slash menu")
	}
	_ = c.Feed([]byte{0x1b})
	act = c.Feed([]byte{'x'})
	if !act.Close {
		t.Fatal("esc did not close the slash menu")
	}
}

func TestSlashMenuAndWorkingGap(t *testing.T) {
	var buf bytes.Buffer
	tr := NewTranscript(&buf, 80)
	tr.SetCompletions([]Completion{
		{Label: "/login", Detail: "Sign in to codex or grok"},
		{Label: "/logout", Detail: "Sign out of codex or grok"},
	}, 0)
	tr.SetInput("/log", 4)
	if err := tr.Paint(); err != nil {
		t.Fatal(err)
	}
	plain := strip(buf.String())
	if !strings.Contains(plain, "/login") || !strings.Contains(plain, "Results 2") || !strings.Contains(plain, "navigate") {
		t.Fatal(plain)
	}
	if strings.Index(plain, "/log") > strings.Index(plain, "Results 2") {
		t.Fatal("menu is above the prompt")
	}
	tr.SetCompletions(nil, 0)
	tr.SetBusy(true)
	if !strings.Contains(strip(buf.String()), "Working") {
		t.Fatal(strip(buf.String()))
	}
	if err := tr.Menu("Sign in", []string{"Sign in with Codex", "Sign in with Grok"}); err != nil {
		t.Fatal(err)
	}
	plain = strip(buf.String())
	if !strings.Contains(plain, "Sign in with Codex") || !strings.Contains(plain, "Sign in with Grok") {
		t.Fatal(plain)
	}
	sel, err := DecideMenu(strings.NewReader("\x1b[B\n"), 2, nil)
	if err != nil || sel != 1 {
		t.Fatalf("sel %d err %v", sel, err)
	}
}

func TestWelcomeLogoIsGX(t *testing.T) {
	var buf bytes.Buffer
	tr := NewTranscript(&buf, 80)
	if err := tr.Welcome("0.0.1"); err != nil {
		t.Fatal(err)
	}
	plain := strip(buf.String())
	if !strings.Contains(plain, Logo+" v0.0.1") {
		t.Fatalf("logo missing: %s", plain)
	}
	if strings.Contains(plain, "𝒇x") || strings.Contains(buf.String(), "fx v") {
		t.Fatal(plain)
	}
	if Logo != "\U0001D488x" {
		t.Fatal(Logo)
	}
}

func TestImageBadge(t *testing.T) {
	got := ImageBadge(2, "/tmp/pic.png")
	want := "\x1b]8;;file:///tmp/pic.png\x1b\\[Image 2]\x1b]8;;\x1b\\"
	if got != want {
		t.Fatalf("%q", got)
	}
	got = ImageBadge(1, "/tmp/a#b?c%d.png")
	if !strings.Contains(got, "file:///tmp/a%23b%3Fc%25d.png") {
		t.Fatal(got)
	}
}

func TestPermissionNote(t *testing.T) {
	choice, note, err := DecidePermission(strings.NewReader("2keep the test\n"), nil)
	if err != nil || choice != 1 || note != "keep the test" {
		t.Fatalf("choice %d note %q err %v", choice, note, err)
	}
	choice, note, err = DecidePermission(strings.NewReader("nope"), nil)
	if err != nil || choice != 2 || note != "" {
		t.Fatalf("deny choice %d note %q err %v", choice, note, err)
	}
	choice, note, err = DecidePermission(strings.NewReader("2use file 2\n"), nil)
	if err != nil || choice != 1 || note != "use file 2" {
		t.Fatalf("noted choice %d note %q err %v", choice, note, err)
	}
}

func replay(s string) string {
	var rows []string
	row, col := 0, 0
	write := func(b byte) {
		for len(rows) <= row {
			rows = append(rows, "")
		}
		line := rows[row]
		for len(line) < col {
			line += " "
		}
		if col < len(line) {
			line = line[:col] + string(b) + line[col+1:]
		} else {
			line += string(b)
		}
		rows[row] = line
		col++
	}
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\n':
			row++
			col = 0
		case '\r':
			col = 0
		default:
			if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
				j := i + 2
				n := 0
				for j < len(s) && s[j] >= '0' && s[j] <= '9' {
					n = n*10 + int(s[j]-'0')
					j++
				}
				if j >= len(s) {
					return strings.Join(rows, "\n")
				}
				switch s[j] {
				case 'A':
					if n == 0 {
						n = 1
					}
					row -= n
					if row < 0 {
						row = 0
					}
				case 'B':
					if n == 0 {
						n = 1
					}
					row += n
				case 'G':
					if n < 1 {
						n = 1
					}
					col = n - 1
				case 'K':
					if row < len(rows) && col < len(rows[row]) {
						rows[row] = rows[row][:col]
					}
				case 'M':
					if n == 0 {
						n = 1
					}
					if row < len(rows) {
						end := row + n
						if end > len(rows) {
							end = len(rows)
						}
						rows = append(rows[:row], rows[end:]...)
					}
				}
				i = j
				continue
			}
			if s[i] == 0x1b {
				continue
			}
			if s[i] >= 0x20 {
				write(s[i])
			}
		}
	}
	return strings.TrimRight(strings.Join(rows, "\n"), "\n")
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

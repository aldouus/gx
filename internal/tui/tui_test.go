package tui

import (
	"bytes"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
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

func TestAltBackspaceDeletesWord(t *testing.T) {
	var c Composer
	c.Feed([]byte("hello world"))
	act := c.Feed([]byte{0x1b, 127})
	if !act.Redraw || c.Line != "hello " || c.Pos != len("hello ") {
		t.Fatalf("line %q pos %d redraw %v", c.Line, c.Pos, act.Redraw)
	}
}

func TestVimNormalDeletesUnderCursor(t *testing.T) {
	var c Composer
	c.Vim = true
	c.Mode = "insert"
	c.Feed([]byte("hi"))
	c.pending = []byte{0x1b}
	if act := c.FinishEsc(); c.Mode != "normal" || !act.Redraw {
		t.Fatalf("mode %s", c.Mode)
	}
	c.Feed([]byte("x"))
	if c.Line != "h" {
		t.Fatalf("line %q", c.Line)
	}
}

func TestVimUndoRedoAndVisualHighlight(t *testing.T) {
	var c Composer
	c.Vim = true
	c.Mode = "insert"
	c.Feed([]byte("hello"))
	c.pending = []byte{0x1b}
	c.FinishEsc()
	c.Feed([]byte("x"))
	if c.Line != "hell" {
		t.Fatalf("delete %q", c.Line)
	}
	c.Feed([]byte("u"))
	if c.Line != "hello" {
		t.Fatalf("undo %q", c.Line)
	}
	c.Feed([]byte{18})
	if c.Line != "hell" {
		t.Fatalf("redo %q", c.Line)
	}
	c.Pos = 0
	c.Feed([]byte("vll"))
	if c.Mode != "visual" {
		t.Fatalf("mode %s", c.Mode)
	}
	got := highlightRange(c.Line, c.Anchor, c.Pos)
	if !strings.Contains(got, "\x1b[7mhel") && !strings.Contains(got, "\x1b[7mhe") {
		t.Fatalf("highlight %q anchor %d pos %d", got, c.Anchor, c.Pos)
	}
}

func TestStatusVimIndicator(t *testing.T) {
	var buf bytes.Buffer
	tr := NewTranscript(&buf, 40)
	tr.SetStatus("grok", "auto", true)
	tr.SetVim(true, "normal", 0)
	tr.SetInput("hi", 1)
	if err := tr.Paint(); err != nil {
		t.Fatal(err)
	}
	plain := strip(buf.String())
	if !strings.Contains(plain, "normal") {
		t.Fatal(plain)
	}
	if strings.Contains(plain, "esc esc") {
		t.Fatal(plain)
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

func TestToolSummaryAndPathLink(t *testing.T) {
	var buf bytes.Buffer
	tr := NewTranscript(&buf, 80)
	if err := tr.ToolStart("shell", `{"command":"echo \"$HOME\""}`); err != nil {
		t.Fatal(err)
	}
	if err := tr.ToolDone("shell", "ok"); err != nil {
		t.Fatal(err)
	}
	if err := tr.ToolStart("write_file", `{"path":"/Users/waites/Desktop/test.md","content":"blorp"}`); err != nil {
		t.Fatal(err)
	}
	if err := tr.ToolDone("write_file", "wrote"); err != nil {
		t.Fatal(err)
	}
	if err := tr.CommitTools(); err != nil {
		t.Fatal(err)
	}
	plain := strip(buf.String())
	if strings.Contains(plain, "{") || strings.Contains(plain, "content") {
		t.Fatal(plain)
	}
	if !strings.Contains(plain, "Edited a file, ran a command") || !strings.Contains(plain, "Ran echo") || !strings.Contains(plain, "Edited test.md") {
		t.Fatal(plain)
	}
	if !strings.Contains(buf.String(), "file:///Users/waites/Desktop/test.md") {
		t.Fatal(buf.String())
	}
	linked := linkPaths("Created `/Users/waites/Desktop/test.md` with gibberish.")
	if strings.Contains(linked, "`") || !strings.Contains(linked, "test.md") || !strings.Contains(linked, "file:///Users/waites/Desktop/test.md") {
		t.Fatal(linked)
	}
}

func TestThinkingBlinkMatchesFx(t *testing.T) {
	start := time.Unix(0, 0)
	if !thinkingBlink(start, start) || !thinkingBlink(start, start.Add(499*time.Millisecond)) {
		t.Fatal("marker should be lit for the first half second")
	}
	if thinkingBlink(start, start.Add(500*time.Millisecond)) || thinkingBlink(start, start.Add(999*time.Millisecond)) {
		t.Fatal("marker should be dark for the second half second")
	}
	if !thinkingBlink(start, start.Add(time.Second)) {
		t.Fatal("marker should relight when the seconds digit advances")
	}
	lit := thinkingRow("Thinking", start, start)
	if !strings.Contains(lit, "\u2022 Thinking") || strings.Contains(lit, "(0s)") {
		t.Fatal(lit)
	}
	dark := thinkingRow("Thinking", start, start.Add(500*time.Millisecond))
	if strings.Contains(dark, "\u2022") || !strings.Contains(dark, "Thinking") {
		t.Fatal(dark)
	}
	counter := thinkingRow("Thinking", start, start.Add(5*time.Second))
	if !strings.Contains(counter, "Thinking (5s)") {
		t.Fatal(counter)
	}
	if formatElapsed(18*60) != "18m0s" || formatElapsed(3663) != "1h1m3s" {
		t.Fatal(formatElapsed(18*60), formatElapsed(3663))
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
	if !strings.Contains(strip(buf.String()), "Thinking") {
		t.Fatal(strip(buf.String()))
	}
	tr.SetBusy(false)
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
	defer tr.SetBusy(false)
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
	if !strings.Contains(plain, "Read a file, searched") || !strings.Contains(plain, "Searched List") || !strings.Contains(plain, "Read handlers.go") {
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
	if !strings.Contains(strip(buf.String()), "Thinking") {
		t.Fatal(strip(buf.String()))
	}
	tr.SetBusy(false)
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

func TestKittyImageUsesPNGPlacement(t *testing.T) {
	png := make([]byte, 24)
	copy(png, []byte("\x89PNG\r\n\x1a\n"))
	png[16], png[17], png[18], png[19] = 0, 0, 0, 10
	png[20], png[21], png[22], png[23] = 0, 0, 0, 20
	w, h, ok := pngSize(png)
	if !ok || w != 10 || h != 20 {
		t.Fatalf("size %d %d %v", w, h, ok)
	}
	seq := kittyPNG(png, 7, 12, 6)
	if !strings.HasPrefix(seq, "\x1b_Ga=T,f=100,t=d,q=2,i=7,c=12,r=6,m=0;") || !strings.HasSuffix(seq, "\x1b\\") {
		t.Fatal(seq)
	}
	if strings.Contains(seq, "\n") {
		t.Fatal("payload must stay on one graphics command")
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
			if i+1 < len(s) && s[i+1] == ']' {
				i += 2
				for i < len(s) && s[i] != 0x07 && !(s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\') {
					i++
				}
				if i < len(s) && s[i] == 0x1b {
					i++
				}
				continue
			}
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

func TestToolRowsHidePayloads(t *testing.T) {
	var buf bytes.Buffer
	tr := NewTranscript(&buf, 80)
	stdout := "<stdout>\nfailed to parse something\n11 ./cmd/gx/main.go\n</stdout>\nexit_code: 0\n"
	if err := tr.ToolStart("shell", `{"command":"find . -name '*.go'"}`); err != nil {
		t.Fatal(err)
	}
	if err := tr.ToolDone("shell", stdout); err != nil {
		t.Fatal(err)
	}
	detail := `{"action":"message","agent":"review-agent","message":"You are reviewing the tool layer"}`
	if err := tr.ToolStart("subagent", detail); err != nil {
		t.Fatal(err)
	}
	if err := tr.ToolDone("subagent", "subagent review-agent started"); err != nil {
		t.Fatal(err)
	}
	if err := tr.ToolStart("glob_files", `{"pattern":"**/*.{rs,toml,md}"}`); err != nil {
		t.Fatal(err)
	}
	if err := tr.ToolDone("glob_files", "cmd/gx/main.go"); err != nil {
		t.Fatal(err)
	}
	if err := tr.CommitTools(); err != nil {
		t.Fatal(err)
	}
	plain := strip(buf.String())
	for _, bad := range []string{"<stdout>", `"action"`, `"command"`, "failed to parse", "Failed "} {
		if strings.Contains(plain, bad) {
			t.Fatalf("row showed %q\n%s", bad, plain)
		}
	}
	for _, want := range []string{"Ran find . -name '*.go'", "Messaged review-agent", "Searched **/*.{rs,toml,md}", "messaged a subagent"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("missing %q\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "You are reviewing the tool layer") {
		t.Fatalf("subagent message was on the row\n%s", plain)
	}
	read := "<path>tui_test.go</path>\n<content>\nexit_code: 1\nerror_code: 7\n</content>\n"
	if toolFailed(ToolLine{Name: "read_file", Detail: `{"path":"tui_test.go"}`, Output: read}) {
		t.Fatal("file body with exit_code looked failed")
	}
	if toolFailed(ToolLine{Name: "read_file", Output: "error_code: 1"}) {
		t.Fatal("error_code is not its own word")
	}
	if !toolFailed(ToolLine{Name: "read_file", Output: "error: missing"}) {
		t.Fatal("a one-line error should fail")
	}
	failed := "<stdout>\nlisting\n</stdout>\n<stderr>\nls: missing: No such file or directory\n</stderr>\nexit_code: 1\n"
	row := toolRow(ToolLine{Name: "shell", Detail: `{"command":"ls missing"}`, Output: failed})
	if strings.Contains(row, "<stdout>") || strings.Contains(row, "<stderr>") || !strings.Contains(row, "Exited 1 ls missing") || !strings.Contains(row, "No such file") {
		t.Fatal(row)
	}
}

func TestComposerWrapsInsteadOfStacking(t *testing.T) {
	var buf bytes.Buffer
	const cols = 20
	tr := NewTranscript(&buf, cols)
	tr.SetVim(true, "insert", 0)
	tr.SetStatus("m", "auto", true)
	line := strings.Repeat("e", 40)
	tr.SetInput(line, len(line))
	rows, rel, col := tr.composerRows()
	if len(rows) < 2 {
		t.Fatalf("expected a wrap, got %d rows", len(rows))
	}
	for _, row := range rows {
		if n := utf8.RuneCountInString(strip(row)); n > cols {
			t.Fatalf("row width %d %q", n, strip(row))
		}
	}
	if rel != len(rows)-1 || col < 1 || col > cols {
		t.Fatalf("cursor row %d col %d rows %d", rel, col, len(rows))
	}
	room := cols - utf8.RuneCountInString(composerMark)
	tr.SetInput(strings.Repeat("e", room), room)
	rows, rel, col = tr.composerRows()
	if len(rows) != 1 || rel != 0 || col != cols {
		t.Fatalf("full row rows=%d rel=%d col=%d", len(rows), rel, col)
	}
	if err := tr.Paint(); err != nil {
		t.Fatal(err)
	}
	before := buf.Len()
	if err := tr.Paint(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String()[before:], "\n") {
		t.Fatal("repainting a full row stacked a copy of the line")
	}
	tr.SetVim(false, "", 0)
	tr.SetInput(strings.Repeat("w", room), room/2)
	rows, _, _ = tr.composerRows()
	if len(rows) != 1 {
		t.Fatalf("caret split the word %#v", rows)
	}
	text := strings.Repeat("a", room-1) + " hello"
	tr.SetVim(true, "insert", 0)
	tr.SetInput(text, len(text))
	rows, _, _ = tr.composerRows()
	if len(rows) < 2 || !strings.HasPrefix(strings.TrimSpace(strip(rows[1])), "hello") {
		t.Fatalf("word wrap %#v", rows)
	}
}

func TestShiftEnterInsertsNewline(t *testing.T) {
	var c Composer
	c.Feed([]byte("hi"))
	act := c.Feed([]byte("\x1b[27;2;13~"))
	if act.Submit != "" || c.Line != "hi\n" || strings.Contains(c.Line, "[27") {
		t.Fatalf("modifyOtherKeys line %q submit %q", c.Line, act.Submit)
	}
	c.Feed([]byte("there"))
	if c.Line != "hi\nthere" {
		t.Fatalf("after text %q", c.Line)
	}
	act = c.Feed([]byte("\r"))
	if act.Submit != "hi\nthere" {
		t.Fatalf("submit %q", act.Submit)
	}

	c = Composer{}
	c.Feed([]byte("ab"))
	c.Feed([]byte("\x1b[13;2u"))
	if c.Line != "ab\n" {
		t.Fatalf("kitty line %q", c.Line)
	}
	before := c.Line
	c.Feed([]byte("\x1b[13;2:3u"))
	if c.Line != before {
		t.Fatalf("release typed %q", c.Line)
	}

	c = Composer{Vim: true, Mode: "normal", Line: "hi", Pos: 2}
	c.Feed([]byte("\x1b[27;2;13~"))
	if c.Line != "hi" || strings.Contains(c.Line, "[27") {
		t.Fatalf("vim normal %q", c.Line)
	}

	c = Composer{}
	c.Feed([]byte("hi"))
	act = c.Feed([]byte("\x1b[13;5u"))
	if act.Submit != "hi" || strings.Contains(c.Line, "[13") {
		t.Fatalf("ctrl+enter submit %q line %q", act.Submit, c.Line)
	}
	c = Composer{}
	c.Feed([]byte("hi"))
	c.Feed([]byte{0x1b, '\r'})
	if c.Line != "hi\n" {
		t.Fatalf("alt+enter %q", c.Line)
	}
	c = Composer{}
	c.Feed([]byte("ab"))
	c.Feed([]byte("\x1b[57414;2u"))
	if c.Line != "ab\n" {
		t.Fatalf("keypad %q", c.Line)
	}
	c = Composer{}
	c.Feed([]byte("hi"))
	c.Feed([]byte{0x1b, '[', '2'})
	if strings.Contains(c.Line, "[") || strings.Contains(c.Line, "2") {
		t.Fatalf("partial sequence typed %q", c.Line)
	}
	c.Feed([]byte("7;2;13~"))
	if c.Line != "hi\n" {
		t.Fatalf("split shift+enter %q", c.Line)
	}

	var buf bytes.Buffer
	tr := NewTranscript(&buf, 40)
	tr.SetStatus("m", "auto", true)
	tr.SetInput("hello\nworld", len("hello\n"))
	rows, rel, _ := tr.composerRows()
	if len(rows) < 2 || rel != 1 {
		t.Fatalf("rows %d rel %d", len(rows), rel)
	}
	for _, row := range rows {
		if strings.Contains(row, "\n") {
			t.Fatalf("newline leaked into a frame row %q", row)
		}
	}
	if !strings.Contains(strip(rows[0]), "hello") || !strings.Contains(strip(rows[1]), "world") {
		t.Fatalf("rows %#v", rows)
	}
}

func TestMarkdownTableRenders(t *testing.T) {
	src := "About the tree.\n\n| Scope | Lines |\n|---|---:|\n| Production Go (`*.go`, excluding `*_test.go`) | 22,990 |\n| Tests | 5,019 |\n\nAfter the table."
	rows := assistantRows(src, 80)
	plain := strip(strings.Join(rows, "\n"))
	if strings.Contains(plain, "|---|") || strings.Contains(plain, "| Scope") || strings.Contains(plain, "`*.go`") {
		t.Fatalf("raw markdown left in table\n%s", plain)
	}
	if !strings.Contains(plain, "Scope") || !strings.Contains(plain, "22,990") || !strings.Contains(plain, "│") || !strings.Contains(plain, "┼") || !strings.Contains(plain, "After the table") {
		t.Fatalf("missing rendered table\n%s", plain)
	}
	var numLines []string
	for _, row := range rows {
		if strings.Contains(strip(row), "22,990") || strings.Contains(strip(row), "5,019") {
			numLines = append(numLines, strip(row))
		}
		if n := len([]rune(strip(row))); n >= 80 {
			t.Fatalf("row wider than the screen %d %q", n, strip(row))
		}
	}
	if len(numLines) != 2 {
		t.Fatalf("number rows %#v", numLines)
	}
	end := func(row, num string) int { return strings.LastIndex(row, num) + len(num) }
	if end(numLines[0], "22,990") != end(numLines[1], "5,019") {
		t.Fatalf("numbers not right aligned\n%q\n%q", numLines[0], numLines[1])
	}

	prose := "Use a | b in a sentence.\nThen more.\n"
	proseRows := assistantRows(prose, 80)
	prosePlain := strip(strings.Join(proseRows, "\n"))
	if strings.Contains(prosePlain, "│") || strings.Contains(prosePlain, "┼") || !strings.Contains(prosePlain, "a | b") || !strings.Contains(prosePlain, "Then more") {
		t.Fatalf("prose pipe became a table\n%s", prosePlain)
	}

	var buf bytes.Buffer
	tr := NewTranscript(&buf, 80)
	tr.SetStatus("m", "auto", true)
	for _, part := range []string{"Use a | b here.\n", "Then more.\n"} {
		if err := tr.Token(part); err != nil {
			t.Fatal(err)
		}
	}
	if err := tr.FinishText(); err != nil {
		t.Fatal(err)
	}
	got := strip(replay(buf.String()))
	if strings.Contains(got, "│") || !strings.Contains(got, "a | b") || !strings.Contains(got, "Then more") {
		t.Fatalf("stream held a prose pipe\n%s", got)
	}

	buf.Reset()
	tr = NewTranscript(&buf, 80)
	tr.SetStatus("m", "auto", true)
	for _, part := range []string{"| Scope | Lines |\n", "|---|---|\n", "| Go | 10 |\n", "\nDone\n"} {
		if err := tr.Token(part); err != nil {
			t.Fatal(err)
		}
	}
	if err := tr.FinishText(); err != nil {
		t.Fatal(err)
	}
	got = strip(replay(buf.String()))
	if strings.Contains(got, "|---|") || strings.Contains(got, "| Scope") {
		t.Fatalf("stream left raw pipes\n%s", got)
	}
	if !strings.Contains(got, "Scope") || !strings.Contains(got, "Go") || !strings.Contains(got, "Done") {
		t.Fatalf("stream missing table\n%s", got)
	}
}

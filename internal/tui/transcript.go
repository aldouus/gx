package tui

import (
	"strings"

	"gx/internal/width"
)

const (
	reset = "\x1b[0m"
	dim   = "\x1b[38;5;245m"
	divC  = "\x1b[38;5;240m"
	bold  = "\x1b[1m"
	rail  = "\x1b[38;5;255m"
)

// ToolLine is one tool row in the frame.
type ToolLine struct {
	Name    string
	Detail  string
	Output  string
	Running bool
}

// Transcript is the scrollback plus the live footer.
type Transcript struct {
	screen *Screen
	cols   int

	model  string
	mode   string
	hasKey bool
	busy   bool
	ctrlC  bool
	detail bool
	alt    bool

	input  string
	cursor int

	live    string
	archive []string
	tools   []ToolLine
	ask     *ToolLine
}

func NewTranscript(out interface{ Write([]byte) (int, error) }, cols int) *Transcript {
	if cols < 20 {
		cols = 80
	}
	return &Transcript{screen: NewScreen(out), cols: cols, cursor: 0}
}

func (t *Transcript) SetStatus(model, mode string, hasKey bool) {
	t.model = model
	t.mode = mode
	t.hasKey = hasKey
}

func (t *Transcript) SetBusy(busy bool)   { t.busy = busy }
func (t *Transcript) SetCtrlC(armed bool) { t.ctrlC = armed }

func (t *Transcript) ToggleDetail() {
	t.detail = !t.detail
	if t.detail {
		t.enterTranscript()
		return
	}
	t.leaveTranscript()
}

func (t *Transcript) enterTranscript() {
	t.alt = true
	t.screen.Reset()
	var b strings.Builder
	b.WriteString("\x1b[?1049h\x1b[H")
	if len(t.archive) == 0 {
		b.WriteString(dim + "transcript is empty" + reset + "\r\n")
	}
	for _, line := range t.archive {
		b.WriteString(line)
		b.WriteString("\r\n")
	}
	for _, tool := range t.tools {
		if tool.Output == "" {
			continue
		}
		b.WriteString(tool.Name)
		b.WriteString("\r\n")
		b.WriteString(tool.Output)
		if !strings.HasSuffix(tool.Output, "\n") {
			b.WriteString("\r\n")
		}
	}
	b.WriteString(dim + "ctrl+o closes the transcript" + reset + "\r\n")
	_ = t.screen.WriteRaw(b.String())
}

func (t *Transcript) leaveTranscript() {
	t.alt = false
	t.screen.Reset()
	_ = t.screen.WriteRaw("\x1b[?1049l")
}

func (t *Transcript) SetInput(line string, cursor int) {
	t.input = line
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(line) {
		cursor = len(line)
	}
	t.cursor = cursor
}

// User commits a person message into scrollback.
func (t *Transcript) User(text string) error {
	var lines []string
	for _, line := range wrap(text, t.cols-2) {
		lines = append(lines, rail+"┃ "+reset+line)
	}
	if len(lines) == 0 {
		lines = []string{rail + "┃" + reset}
	}
	t.archive = append(t.archive, text)
	return t.screen.Commit(lines)
}

// Token appends streamed assistant text. A finished line leaves the frame
// and stays in scrollback.
func (t *Transcript) Token(s string) error {
	t.live += s
	if err := t.Paint(); err != nil {
		return err
	}
	for {
		i := strings.IndexByte(t.live, '\n')
		if i < 0 {
			return nil
		}
		line := t.live[:i]
		n := len(wrapPlain(line, t.cols))
		t.live = t.live[i+1:]
		t.screen.Release(n)
	}
}

// FinishText keeps the assistant text in scrollback and drops it from the frame.
func (t *Transcript) FinishText() error {
	if t.live == "" {
		return t.Paint()
	}
	if err := t.Paint(); err != nil {
		return err
	}
	if t.live != "" {
		t.archive = append(t.archive, t.live)
	}
	n := len(wrapPlain(t.live, t.cols))
	t.live = ""
	t.screen.Release(n)
	return nil
}

// ToolStart shows a running tool row.
func (t *Transcript) ToolStart(name, detail string) error {
	t.tools = append(t.tools, ToolLine{Name: name, Detail: detail, Running: true})
	return t.Paint()
}

// ToolDone finishes the matching tool row.
func (t *Transcript) ToolDone(name, output string) error {
	for i := len(t.tools) - 1; i >= 0; i-- {
		if t.tools[i].Name == name && t.tools[i].Running {
			t.tools[i].Running = false
			t.tools[i].Output = output
			break
		}
	}
	return t.Paint()
}

// Permission shows an approval prompt.
func (t *Transcript) Permission(name, detail string) error {
	t.ask = &ToolLine{Name: name, Detail: detail}
	return t.Paint()
}

// ClearPermission removes the approval prompt.
func (t *Transcript) ClearPermission() error {
	t.ask = nil
	return t.Paint()
}

// Paint redraws the frame. The alternate transcript owns the screen until it closes.
func (t *Transcript) Paint() error {
	if t.alt {
		return nil
	}
	frame := t.frame()
	return t.screen.Paint(frame)
}

func (t *Transcript) frame() Frame {
	var rows []string
	if t.live != "" {
		rows = append(rows, wrapPlain(t.live, t.cols)...)
	}
	for _, tool := range t.tools {
		label := "Ran"
		if tool.Running {
			label = "Running"
		}
		rows = append(rows, dim+label+" "+tool.Name+"  "+width.Clip(oneLine(tool.Detail), t.cols-len(label)-len(tool.Name)-4)+reset)
		if tool.Output != "" {
			out := tool.Output
			if !t.detail {
				if i := strings.IndexByte(out, '\n'); i >= 0 {
					out = out[:i]
				}
				out = width.Clip(out, t.cols-2)
			}
			for _, line := range strings.Split(out, "\n") {
				rows = append(rows, dim+"  "+line+reset)
			}
		}
	}
	if t.ask != nil {
		rows = append(rows, bold+"Allow "+t.ask.Name+"?"+reset)
		rows = append(rows, "  "+width.Clip(oneLine(t.ask.Detail), t.cols-2))
		rows = append(rows, dim+"  y / n"+reset)
	}
	rows = append(rows, divC+strings.Repeat("─", t.cols)+reset)
	inputRow := len(rows)
	rows = append(rows, t.inputRow())
	rows = append(rows, t.hintRow())
	col := 3 + width.String(t.input[:t.cursor])
	if col < 1 {
		col = 1
	}
	return Frame{Rows: rows, CursorRow: inputRow, CursorCol: col}
}

func (t *Transcript) inputRow() string {
	shown := t.input
	cur := t.cursor
	if cur > len(shown) {
		cur = len(shown)
	}
	return "❯ " + shown[:cur] + "\x1b[7m \x1b[0m" + shown[cur:]
}

func (t *Transcript) hintRow() string {
	if t.ctrlC {
		return dim + width.Clip("press ctrl+c again to exit", t.cols) + reset
	}
	var left []string
	if !t.hasKey {
		left = append(left, "run /login")
	}
	if t.mode != "" {
		left = append(left, t.mode)
	}
	if t.model != "" {
		left = append(left, t.model)
	}
	text := strings.Join(left, " · ")
	right := ""
	if t.busy {
		right = "esc esc to interrupt"
	}
	if right != "" && width.String(text)+width.String(right)+1 < t.cols {
		gap := t.cols - width.String(text) - width.String(right)
		if gap < 1 {
			gap = 1
		}
		text = text + strings.Repeat(" ", gap) + right
	}
	return dim + width.Clip(text, t.cols) + reset
}

func wrap(text string, cols int) []string {
	if cols < 8 {
		cols = 8
	}
	if text == "" {
		return nil
	}
	var out []string
	for _, para := range strings.Split(text, "\n") {
		for len(para) > 0 {
			cut := width.Prefix(para, cols)
			if cut == "" {
				_, size := firstRune(para)
				cut = para[:size]
			}
			out = append(out, cut)
			para = para[len(cut):]
			para = strings.TrimPrefix(para, " ")
		}
	}
	return out
}

func wrapPlain(text string, cols int) []string {
	lines := wrap(text, cols)
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}

func firstRune(s string) (rune, int) {
	if s == "" {
		return 0, 0
	}
	return rune(s[0]), 1
}

package tui

import (
	"encoding/base64"
	"strings"

	"gx/internal/width"
)

const (
	reset = "\x1b[0m"
	dim   = "\x1b[38;5;245m"
	bold  = "\x1b[1m"
	rail  = "\x1b[38;5;255m"

	// Logo is the welcome wordmark. The mathematical italic g is this port's
	// mark. It is not fx's 𝒇x.
	Logo = "\U0001D488x"
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
	cwd    string
	effort string
	hasKey bool
	busy   bool
	ctrlC  bool
	detail bool
	alt    bool

	input  string
	cursor int

	live          string
	archive       []string
	tools         []ToolLine
	ask           *ToolLine
	askChoice     int
	askNote       string
	askAmend      bool
	comps         []Completion
	compSel       int
	menu          *menuState
	statusSeq     string
	result        string
	lastAssistant string
	hideContext   bool
	hideSession   bool
	hideWorkspace bool
	fast          bool
}

// SetFast shows or hides the fast-mode marker in the status line.
func (t *Transcript) SetFast(on bool) {
	if t == nil {
		return
	}
	t.fast = on
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

// LastAssistant is the last finished assistant text, for /copy.
func (t *Transcript) LastAssistant() string {
	if t == nil {
		return ""
	}
	return t.lastAssistant
}

// ToggleStatus flips a footer segment: context, session, or workspace.
func (t *Transcript) ToggleStatus(part string) string {
	if t == nil {
		return "no transcript"
	}
	switch strings.TrimSpace(part) {
	case "", "status":
		return t.statusSummary()
	case "context":
		t.hideContext = !t.hideContext
	case "session":
		t.hideSession = !t.hideSession
	case "workspace":
		t.hideWorkspace = !t.hideWorkspace
	default:
		return "usage: /statusline [context|session|workspace]"
	}
	return t.statusSummary()
}

func (t *Transcript) statusSummary() string {
	on := func(hidden bool) string {
		if hidden {
			return "off"
		}
		return "on"
	}
	return "context " + on(t.hideContext) + "\nsession " + on(t.hideSession) + "\nworkspace " + on(t.hideWorkspace)
}

// SetPlace records the workspace and effort shown in the footer.
func (t *Transcript) SetPlace(cwd, effort string) {
	t.cwd = cwd
	if effort != "" {
		t.effort = effort
	}
}

func (t *Transcript) SetBusy(busy bool) {
	t.busy = busy
	if busy {
		t.result = ""
	}
	t.noteActivity()
	if !t.alt {
		_ = t.Paint()
	}
}

// SetResult records how the last turn ended: done or error.
func (t *Transcript) SetResult(state string) {
	if t == nil {
		return
	}
	t.result = state
	t.noteActivity()
}
func (t *Transcript) SetCtrlC(armed bool) { t.ctrlC = armed }

// End tells the terminal the program is leaving.
func (t *Transcript) End() {
	if t == nil || t.screen == nil {
		return
	}
	_ = t.screen.WriteRaw("\x1b]7501;state=clear\x1b\\")
}

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

// Completion is one slash-command row in the footer menu.
type Completion struct {
	Label  string
	Detail string
	Insert string
	Skill  bool
}

type menuState struct {
	title string
	items []string
	sel   int
}

// SetCompletions shows the slash menu above the composer. An empty list hides it.
func (t *Transcript) SetCompletions(items []Completion, selected int) {
	if t == nil {
		return
	}
	t.comps = items
	if selected < 0 {
		selected = 0
	}
	if len(items) > 0 && selected >= len(items) {
		selected = len(items) - 1
	}
	t.compSel = selected
}

// Menu opens a footer selector. Login uses this before a provider is chosen.
func (t *Transcript) Menu(title string, items []string) error {
	t.menu = &menuState{title: title, items: append([]string{}, items...)}
	return t.Paint()
}

// SetMenu moves the selector.
func (t *Transcript) SetMenu(sel int) error {
	if t.menu == nil {
		return nil
	}
	if sel < 0 {
		sel = 0
	}
	if sel >= len(t.menu.items) {
		sel = len(t.menu.items) - 1
	}
	t.menu.sel = sel
	return t.Paint()
}

// ClearMenu closes the selector.
func (t *Transcript) ClearMenu() error {
	t.menu = nil
	return t.Paint()
}

// Welcome paints the gx wordmark once, above the first prompt.
func (t *Transcript) Welcome(version string) error {
	if version == "" {
		version = "0.0.1"
	}
	line := bold + Logo + reset + dim + " v" + version + " · Run /help for commands" + reset
	t.archive = append(t.archive, Logo+" v"+version)
	return t.screen.Commit([]string{line, ""})
}

// ShowSignIn prints a sign-in prompt. Every wrapped piece of the URL is a
// link to the full address, so a click on any line opens the same page.
func (t *Transcript) ShowSignIn(msg string) error {
	for _, line := range strings.Split(msg, "\n") {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "http://") || strings.HasPrefix(line, "https://") || strings.HasPrefix(line, "\x1b]8;;") {
			if err := t.commitLinked(line); err != nil {
				return err
			}
			continue
		}
		if err := t.User(line); err != nil {
			return err
		}
	}
	return nil
}

func (t *Transcript) commitLinked(line string) error {
	url, text := splitLink(line)
	if url == "" {
		url = text
	}
	cols := t.cols - 2
	if cols < 16 {
		cols = 16
	}
	var lines []string
	rest := text
	for rest != "" {
		n := cols
		if n > len(rest) {
			n = len(rest)
		}
		chunk := rest[:n]
		rest = rest[n:]
		lines = append(lines, rail+"\u2503 "+reset+"\x1b]8;;"+url+"\x1b\\"+chunk+"\x1b]8;;\x1b\\")
	}
	t.archive = append(t.archive, text)
	return t.screen.Commit(lines)
}

func splitLink(line string) (url, text string) {
	const open = "\x1b]8;;"
	if !strings.HasPrefix(line, open) {
		return "", line
	}
	rest := line[len(open):]
	url, rest, ok := strings.Cut(rest, "\x1b\\")
	if !ok {
		return "", line
	}
	text = strings.TrimSuffix(rest, "\x1b]8;;\x1b\\")
	return url, text
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
		t.lastAssistant = t.live
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

// Permission shows an approval prompt. Choice 0 is allow once.
func (t *Transcript) Permission(name, detail string) error {
	t.ask = &ToolLine{Name: name, Detail: detail}
	t.askChoice = 0
	t.askNote = ""
	t.noteActivity()
	return t.Paint()
}

// SetPermission updates the selected choice and the note while the prompt is open.
func (t *Transcript) SetPermission(choice int, note string) error {
	return t.SetPermissionAmend(choice, note, false)
}

// SetPermissionAmend updates the choice, the note, and whether tab has opened
// the "tell fx" line.
func (t *Transcript) SetPermissionAmend(choice int, note string, amend bool) error {
	if t.ask == nil {
		return nil
	}
	if choice < 0 {
		choice = 0
	}
	if choice > 2 {
		choice = 2
	}
	t.askChoice = choice
	t.askNote = note
	t.askAmend = amend
	return t.Paint()
}

// ClearPermission removes the approval prompt.
func (t *Transcript) ClearPermission() error {
	t.ask = nil
	t.askChoice = 0
	t.askNote = ""
	t.askAmend = false
	t.noteActivity()
	return t.Paint()
}

// Paint redraws the frame. The alternate transcript owns the screen until it closes.
func (t *Transcript) Paint() error {
	if t.alt {
		return nil
	}
	t.noteActivity()
	frame := t.frame()
	return t.screen.Paint(frame)
}

// ProgramStatus is an OSC 7501 report. Terminals that do not implement it ignore the sequence.
func ProgramStatus(state, kind, msg string) string {
	var b strings.Builder
	b.WriteString("\x1b]7501;state=")
	b.WriteString(state)
	if kind != "" {
		b.WriteString(":kind=")
		b.WriteString(kind)
	}
	b.WriteString(":app=gx")
	if msg != "" {
		b.WriteString(":msg=")
		b.WriteString(base64.StdEncoding.EncodeToString([]byte(oneLine(msg))))
	}
	b.WriteString("\x1b\\")
	return b.String()
}

func (t *Transcript) noteActivity() {
	if t == nil || t.screen == nil || t.alt {
		return
	}
	state, kind, msg := "idle", "", ""
	switch {
	case t.ask != nil:
		state, kind, msg = "blocked", "permission", t.ask.Name+" "+t.ask.Detail
	case t.busy:
		state = "working"
	case t.result == "error":
		state = "error"
	case t.result == "done":
		state = "done"
	}
	seq := ProgramStatus(state, kind, msg)
	if seq == t.statusSeq {
		return
	}
	t.statusSeq = seq
	_ = t.screen.WriteRaw(seq)
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
		return t.permissionFrame(rows)
	}
	rows = append(rows, "")
	if t.busy {
		rows = append(rows, dim+"\u23fa Working"+reset)
	}
	if t.menu != nil {
		return t.menuFrame(rows)
	}
	inputRow := len(rows)
	rows = append(rows, t.inputRow())
	rows = append(rows, t.completionRows()...)
	if len(t.comps) == 0 {
		rows = append(rows, t.hintRow())
	}
	col := width.String(composerMark) + width.String(t.input[:t.cursor]) + 1
	if col < 1 {
		col = 1
	}
	return Frame{Rows: rows, CursorRow: inputRow, CursorCol: col}
}

const composerMark = "┃ "

func (t *Transcript) permissionFrame(rows []string) Frame {
	rows = append(rows, bold+"Allow "+t.ask.Name+"?"+reset)
	if t.ask.Detail != "" {
		rows = append(rows, dim+"  "+width.Clip(oneLine(t.ask.Detail), t.cols-2)+reset)
	}
	labels := t.choiceLabels()
	choiceRow := len(rows)
	for i, label := range labels {
		mark := "  "
		if i == t.askChoice {
			mark = composerMark
			rows = append(rows, bold+mark+label+reset)
			continue
		}
		rows = append(rows, dim+mark+label+reset)
	}
	noteRow := len(rows)
	if t.askAmend || t.askNote != "" {
		rows = append(rows, t.noteRow())
	}
	rows = append(rows, t.permissionHint())
	cursorRow := choiceRow + t.askChoice
	cursorCol := width.String(composerMark) + 1
	if t.askAmend || t.askNote != "" {
		cursorRow = noteRow
		cursorCol = width.String(composerMark) + width.String(t.askNote) + 1
	}
	return Frame{Rows: rows, CursorRow: cursorRow, CursorCol: cursorCol}
}

func (t *Transcript) choiceLabels() []string {
	labels := []string{"1. Allow once", "2. Allow always", "3. Deny"}
	if t.askAmend || t.askNote != "" {
		switch t.askChoice {
		case 0:
			labels[0] = "1. Yes, and tell gx what to do next"
		case 2:
			labels[2] = "3. No, and tell gx what to do differently"
		}
	}
	return labels
}

func (t *Transcript) noteRow() string {
	shown := t.askNote
	if shown == "" {
		placeholder := "and tell gx what to do next"
		if t.askChoice == 2 {
			placeholder = "and tell gx what to do differently"
		}
		return composerMark + dim + placeholder + reset
	}
	return composerMark + shown + "\x1b[7m \x1b[0m"
}

func (t *Transcript) permissionHint() string {
	return dim + width.Clip("1-3 choose   arrows move   tab amend   enter confirm   y once   n deny", t.cols) + reset
}

func (t *Transcript) completionRows() []string {
	if len(t.comps) == 0 {
		return nil
	}
	rule := dim + strings.Repeat("\u2500", t.cols) + reset
	n := len(t.comps)
	start := 0
	if n > 6 {
		start = t.compSel - 2
		if start < 0 {
			start = 0
		}
		if start+6 > n {
			start = n - 6
		}
	}
	end := start + 6
	if end > n {
		end = n
	}
	nameWidth := 0
	for _, c := range t.comps[start:end] {
		if w := width.String(c.Label); w > nameWidth {
			nameWidth = w
		}
	}
	rows := []string{rule, dim + fmtCount(n) + reset, ""}
	for i := start; i < end; i++ {
		c := t.comps[i]
		mark := "  "
		style := dim
		if i == t.compSel {
			mark = "\u203a "
			style = bold
		}
		gap := nameWidth - width.String(c.Label) + 2
		detail := c.Detail
		if room := t.cols - nameWidth - 6; room > 8 {
			detail = width.Clip(detail, room)
		}
		rows = append(rows, style+mark+c.Label+reset+dim+strings.Repeat(" ", gap)+detail+reset)
	}
	rows = append(rows, rule, dim+"\u2191\u2193 navigate     enter use     esc close"+reset)
	return rows
}

func fmtCount(n int) string {
	return "Results " + itoa(n)
}

func (t *Transcript) menuFrame(rows []string) Frame {
	rows = append(rows, bold+t.menu.title+reset)
	choiceRow := len(rows)
	for i, item := range t.menu.items {
		if i == t.menu.sel {
			rows = append(rows, bold+"\u203a "+item+reset)
			continue
		}
		rows = append(rows, dim+"  "+item+reset)
	}
	rows = append(rows, dim+"arrows move   enter confirm   esc cancel"+reset)
	return Frame{Rows: rows, CursorRow: choiceRow + t.menu.sel, CursorCol: 3}
}

func (t *Transcript) inputRow() string {
	shown := t.input
	cur := t.cursor
	if cur > len(shown) {
		cur = len(shown)
	}
	return composerMark + shown[:cur] + "\x1b[7m \x1b[0m" + shown[cur:]
}

func (t *Transcript) hintRow() string {
	if t.ctrlC {
		return dim + width.Clip("press ctrl+c again to exit", t.cols) + reset
	}
	var left []string
	if !t.hasKey {
		left = append(left, "run /login")
	}
	if !t.hideSession {
		if t.mode != "" {
			left = append(left, t.mode)
		}
		if t.model != "" {
			left = append(left, t.model)
		}
	}
	effort := t.effort
	if effort == "" {
		effort = "medium"
	}
	left = append(left, effort)
	if t.fast {
		left = append(left, "\u26a1\ufe0e")
	}
	if !t.hideContext {
		if label := t.contextLabel(); label != "0" {
			left = append(left, label)
		}
	}
	if t.cwd != "" && !t.hideWorkspace {
		left = append(left, filepathBase(t.cwd))
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

func (t *Transcript) contextLabel() string {
	n := len(t.live)
	for _, line := range t.archive {
		n += len(line)
	}
	tokens := n / 4
	if tokens >= 1000 {
		whole := tokens / 1000
		frac := (tokens % 1000) / 100
		return itoa(whole) + "." + itoa(frac) + "k"
	}
	return itoa(tokens)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [16]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func filepathBase(path string) string {
	path = strings.TrimRight(path, "/")
	if path == "" {
		return "/"
	}
	if i := strings.LastIndex(path, "/"); i >= 0 && i < len(path)-1 {
		return path[i+1:]
	}
	return path
}

func shortPath(path string) string {
	path = strings.TrimRight(path, "/")
	if path == "" {
		return "/"
	}
	if width.String(path) <= 24 {
		return path
	}
	base := path
	if i := strings.LastIndex(path, "/"); i >= 0 && i < len(path)-1 {
		base = path[i+1:]
	}
	if base == "" {
		return width.Clip(path, 24)
	}
	return width.Clip(base, 24)
}

// ImageBadge is an OSC 8 hyperlink around an image label.
func ImageBadge(n int, path string) string {
	if n < 1 {
		n = 1
	}
	return "\x1b]8;;" + fileURL(path) + "\x1b\\[Image " + itoa(n) + "]\x1b]8;;\x1b\\"
}

func fileURL(path string) string {
	var b strings.Builder
	b.WriteString("file://")
	for i := 0; i < len(path); i++ {
		c := path[i]
		if c == '/' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
			continue
		}
		const hex = "0123456789ABCDEF"
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0x0f])
	}
	return b.String()
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

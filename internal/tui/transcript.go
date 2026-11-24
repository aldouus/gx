package tui

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"gx/internal/width"
)

const (
	reset = "\x1b[0m"
	dim   = "\x1b[38;5;245m"
	bold  = "\x1b[1m"
	rail  = "\x1b[38;5;255m"

	// Logo is the welcome wordmark. The mathematical italic g is this port's
	// mark. It is not the upstream 𝒇x.
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
	opened        bool
	archive       []string
	tools         []ToolLine
	toolStart     time.Time
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
	vim           bool
	vimMode       string
	anchor        int
	cursorShape   string

	busyAt   time.Time
	stopTick chan struct{}
	mu       sync.Mutex
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
	return &Transcript{screen: NewScreen(out), cols: cols, cursor: 0, hideContext: true, hideWorkspace: true}
}

func (t *Transcript) SetStatus(model, mode string, hasKey bool) {
	t.model = model
	t.mode = mode
	t.hasKey = hasKey
}

// WriteRaw sends bytes without moving the frame. /copy uses it for the clipboard.
func (t *Transcript) WriteRaw(s string) error {
	if t == nil || t.screen == nil {
		return nil
	}
	return t.screen.WriteRaw(s)
}

// LastAssistant is the last finished assistant text, for /copy.
func (t *Transcript) LastAssistant() string {
	if t == nil {
		return ""
	}
	return t.lastAssistant
}

// SetStatusLine shows or hides each footer segment.
func (t *Transcript) SetStatusLine(context, session, workspace bool) {
	if t == nil {
		return
	}
	t.hideContext = !context
	t.hideSession = !session
	t.hideWorkspace = !workspace
}

// StatusLine reports whether context, session, and workspace are shown.
func (t *Transcript) StatusLine() (context, session, workspace bool) {
	if t == nil {
		return false, true, false
	}
	return !t.hideContext, !t.hideSession, !t.hideWorkspace
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
	_ = t.Paint()
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
	t.mu.Lock()
	t.busy = busy
	var stop chan struct{}
	if busy {
		t.result = ""
		t.busyAt = time.Now()
		stop = make(chan struct{})
		t.stopTick = stop
	} else if t.stopTick != nil {
		close(t.stopTick)
		t.stopTick = nil
		t.busyAt = time.Time{}
	}
	if !t.alt {
		_ = t.paintLocked()
	}
	t.mu.Unlock()
	if stop != nil {
		go t.tick(stop)
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
	t.SetBusy(false)
	_ = t.screen.WriteRaw("\x1b[0 q\x1b]7501;state=clear\x1b\\")
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

// SetVim records whether vim keys are on and which mode the composer is in.
// anchor is the visual-mode selection start.
func (t *Transcript) SetVim(on bool, mode string, anchor int) {
	if t == nil {
		return
	}
	t.vim = on
	if mode == "" {
		mode = "insert"
	}
	t.vimMode = mode
	t.anchor = anchor
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

// ClearView wipes the visible transcript so a resumed chat can replace it.
func (t *Transcript) ClearView() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.archive = nil
	t.live = ""
	t.tools = nil
	t.opened = false
	t.screen.Reset()
	return t.screen.WriteRaw("\x1b[2J\x1b[H")
}

// ShowAssistant commits a finished reply into scrollback.
func (t *Transcript) ShowAssistant(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	lines := assistantRows(text, t.cols)
	lines = append(lines, "")
	t.archive = append(t.archive, text)
	t.lastAssistant = text
	return t.screen.Commit(lines)
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
	lines = append(lines, "")
	t.archive = append(t.archive, text)
	return t.screen.Commit(lines)
}

// Token appends streamed assistant text. A finished line leaves the frame
// and stays in scrollback.
func (t *Transcript) Token(s string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.commitToolsLocked(); err != nil {
		return err
	}
	t.opened = true
	t.live = tighten(t.live + s)
	if err := t.paintLocked(); err != nil {
		return err
	}
	for {
		for strings.HasPrefix(t.live, "\n") {
			t.live = t.live[1:]
		}
		block, rest, ok := committable(t.live)
		if !ok {
			return nil
		}
		n := len(assistantRows(block, t.cols))
		t.live = rest
		t.screen.Release(n)
	}

}

// FinishText keeps the assistant text in scrollback and drops it from the frame.
func (t *Transcript) FinishText() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.opened && t.live == "" {
		return t.paintLocked()
	}
	text := strings.TrimRight(tighten(t.live), "\n")
	if text != "" {
		t.archive = append(t.archive, text)
		t.lastAssistant = text
		t.live = text
		if err := t.paintLocked(); err != nil {
			return err
		}
		n := len(assistantRows(text, t.cols))
		t.live = ""
		t.screen.Release(n)
	} else {
		t.live = ""
	}
	t.opened = false
	if err := t.screen.Commit([]string{""}); err != nil {
		return err
	}
	return t.paintLocked()
}

// ToolStart shows a running tool row.
func (t *Transcript) ToolStart(name, detail string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.tools) == 0 {
		t.toolStart = time.Now()
	}
	t.tools = append(t.tools, ToolLine{Name: name, Detail: detail, Running: true})
	return t.paintLocked()
}

// CommitTools writes the current tool tree into scrollback and clears it.
func (t *Transcript) CommitTools() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.commitToolsLocked()
}

func (t *Transcript) commitToolsLocked() error {
	if t == nil || len(t.tools) == 0 {
		return nil
	}
	lines := t.toolTree()
	if !t.toolStart.IsZero() {
		lines = append(lines, "", dim+"  "+toolSummary(t.tools, time.Since(t.toolStart))+reset)
	}
	lines = append(lines, "")
	t.tools = nil
	t.toolStart = time.Time{}
	return t.screen.Commit(lines)
}

// ToolDone finishes the matching tool row.
func (t *Transcript) ToolDone(name, output string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := len(t.tools) - 1; i >= 0; i-- {
		if t.tools[i].Name == name && t.tools[i].Running {
			t.tools[i].Running = false
			t.tools[i].Output = output
			break
		}
	}
	return t.paintLocked()
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
// the "tell gx" line.
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
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.paintLocked()
}

func (t *Transcript) paintLocked() error {
	if t.alt {
		return nil
	}
	t.noteActivity()
	t.syncCursor()
	frame := t.frame()
	return t.screen.Paint(frame)
}

func (t *Transcript) tick(stop <-chan struct{}) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case now := <-ticker.C:
			_ = now
			_ = t.Paint()
		}
	}
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
		state, kind, msg = "blocked", "permission", t.ask.Name+" "+visibleDetail(t.ask.Name, t.ask.Detail)
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
		rows = append(rows, assistantRows(t.live, t.cols)...)
	}
	rows = append(rows, t.toolTree()...)
	if t.ask != nil {
		return t.permissionFrame(rows)
	}
	if t.busy {
		rows = append(rows, thinkingRow(t.activityPhase(), t.busyAt, time.Now()))
	}
	if t.live != "" || len(t.tools) > 0 || t.busy {
		rows = append(rows, "")
	}
	if t.menu != nil {
		return t.menuFrame(rows)
	}
	inputAt := len(rows)
	wrapped, rel, col := t.composerRows()
	rows = append(rows, wrapped...)
	rows = append(rows, t.completionRows()...)
	if len(t.comps) == 0 {
		rows = append(rows, "", t.hintRow(), "")
	}
	return Frame{Rows: rows, CursorRow: inputAt + rel, CursorCol: col}
}

const composerMark = "┃ "

const thinkingStyle = "\x1b[38;5;252m"

func (t *Transcript) activityPhase() string {
	switch {
	case len(t.tools) > 0:
		return "Running"
	case t.live != "":
		return "Generating"
	default:
		return "Thinking"
	}
}

func thinkingRow(phase string, started, now time.Time) string {
	label := "\u2022 " + phase
	if !started.IsZero() {
		if secs := int(now.Sub(started) / time.Second); secs >= 1 {
			label += " (" + formatElapsed(secs) + ")"
		}
	}
	marker := "\u2022"
	if !started.IsZero() && !thinkingBlink(started, now) {
		marker = " "
	}
	rest := strings.TrimPrefix(label, "\u2022")
	return thinkingStyle + marker + rest + reset
}

func thinkingBlink(started, now time.Time) bool {
	if started.IsZero() || now.Before(started) {
		return true
	}
	half := int(now.Sub(started) / (500 * time.Millisecond))
	return half%2 == 0
}

func formatElapsed(seconds int) string {
	if seconds < 0 {
		seconds = 0
	}
	secs := seconds % 60
	mins := (seconds / 60) % 60
	hours := seconds / 3600
	if hours > 0 {
		return itoa(hours) + "h" + itoa(mins) + "m" + itoa(secs) + "s"
	}
	if mins > 0 {
		return itoa(mins) + "m" + itoa(secs) + "s"
	}
	return itoa(secs) + "s"
}

func (t *Transcript) permissionFrame(rows []string) Frame {
	rows = append(rows, bold+"Allow "+t.ask.Name+"?"+reset)
	if detail := visibleDetail(t.ask.Name, t.ask.Detail); detail != "" {
		rows = append(rows, dim+"  "+width.Clip(detail, t.cols-2)+reset)
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

// composerRows wraps the prompt so a long line stays inside the terminal.
// Later rows use spaces the same width as composerMark.
func (t *Transcript) composerRows() (rows []string, relRow, col int) {
	prefixW := width.String(composerMark)
	if prefixW < 1 {
		prefixW = 1
	}
	// Autowrap is off, so a glyph may end on the last column.
	room := t.cols - prefixW
	if room < 1 {
		room = 1
	}
	parts, relRow, contentCol := packComposer(t.composerCells(), room)
	if len(parts) == 0 {
		parts = []string{""}
	}
	pad := strings.Repeat(" ", prefixW)
	for i, part := range parts {
		if i == 0 {
			rows = append(rows, composerMark+part)
		} else {
			rows = append(rows, pad+part)
		}
	}
	col = prefixW + contentCol + 1
	if col > t.cols {
		col = t.cols
	}
	if col < 1 {
		col = 1
	}
	if relRow < 0 {
		relRow = 0
	}
	if relRow >= len(rows) {
		relRow = len(rows) - 1
	}
	if t.cursor >= len(t.input) && t.caretSpace(t.cursor) && contentCol < room && relRow >= 0 && relRow < len(rows) {
		rows[relRow] += "\x1b[7m \x1b[0m"
	}
	return rows, relRow, col
}

type composerCell struct {
	text         string
	w            int
	word         bool
	cursorBefore bool
	brk          bool
}

func (t *Transcript) composerCells() []composerCell {
	raw := t.input
	cur := t.cursor
	if cur < 0 {
		cur = 0
	}
	if cur > len(raw) {
		cur = len(raw)
	}
	var cells []composerCell
	for i := 0; i < len(raw); {
		_, size := utf8.DecodeRuneInString(raw[i:])
		if size <= 0 {
			break
		}
		text := raw[i : i+size]
		if text == "\n" {
			cells = append(cells, composerCell{
				brk:          true,
				cursorBefore: i == cur,
			})
			i += size
			continue
		}
		cw := width.String(text)
		if cw < 1 {
			cw = 1
		}
		word := isComposerWord(text)
		if t.reverseRune(raw, i, cur) {
			text = "\x1b[7m" + text + "\x1b[0m"
		}
		cells = append(cells, composerCell{
			text:         text,
			w:            cw,
			word:         word,
			cursorBefore: i == cur,
		})
		i += size
	}
	return cells
}

// caretSpace is the reverse-video block shown where there is no character under
// the cursor: the normal composer, and vim normal or an empty visual selection
// at the end of the line. Insert mode uses the terminal cursor instead.
func (t *Transcript) caretSpace(cur int) bool {
	if !t.vim {
		return true
	}
	if t.vimMode == "insert" || cur < len(t.input) {
		return false
	}
	if t.vimMode == "visual" {
		a := t.anchor
		if a < 0 {
			a = 0
		}
		if a < len(t.input) {
			return false
		}
	}
	return true
}

func (t *Transcript) reverseRune(raw string, i, cur int) bool {
	if !t.vim || t.vimMode == "insert" || i >= len(raw) {
		return false
	}
	if t.vimMode != "visual" {
		return i == cur
	}
	a, end := t.anchor, cur
	if a > end {
		a, end = end, a
	}
	if a < 0 {
		a = 0
	}
	if end < len(raw) {
		_, size := utf8.DecodeRuneInString(raw[end:])
		end += size
	}
	if end > len(raw) {
		end = len(raw)
	}
	if a > len(raw) {
		a = len(raw)
	}
	if a >= end {
		return i == cur
	}
	return i >= a && i < end
}

func isComposerWord(text string) bool {
	r, _ := utf8.DecodeRuneInString(text)
	if r == utf8.RuneError && text == "" {
		return false
	}
	return r != ' ' && r != '\t' && r != '\n'
}

func packComposer(cells []composerCell, room int) (lines []string, curRow, curCol int) {
	if room < 1 {
		room = 1
	}
	var b strings.Builder
	col := 0
	row := 0
	placed := false
	place := func() {
		if placed {
			return
		}
		curRow = row
		curCol = col
		placed = true
	}
	breakRow := func() {
		lines = append(lines, b.String())
		b.Reset()
		row++
		col = 0
	}
	for i, cell := range cells {
		if cell.brk {
			if cell.cursorBefore {
				place()
			}
			breakRow()
			continue
		}
		if cell.w > room {
			cell.w = room
		}
		if col > 0 && (col+cell.w > room || composerWordWrap(cells, i, col, room)) {
			breakRow()
		}
		if cell.cursorBefore {
			place()
		}
		b.WriteString(cell.text)
		col += cell.w
	}
	if !placed {
		place()
	}
	if b.Len() > 0 || len(lines) == 0 || curRow >= len(lines) {
		lines = append(lines, b.String())
	}
	return lines, curRow, curCol
}

func composerWordWrap(cells []composerCell, i, col, room int) bool {
	if col <= 0 || !cells[i].word {
		return false
	}
	if i > 0 && cells[i-1].word {
		return false
	}
	w := 0
	for j := i; j < len(cells) && cells[j].word; j++ {
		w += cells[j].w
		if w > room {
			return false
		}
	}
	return w > room-col
}

func vimLabel(mode string) string {
	switch mode {
	case "normal":
		return "normal"
	case "visual":
		return "visual"
	default:
		return "insert"
	}
}

func justifyStatus(left, right string, cols int) string {
	rw := width.String(right)
	if rw >= cols {
		return dim + width.Clip(right, cols) + reset
	}
	room := cols - rw - 1
	if room < 1 {
		room = 1
	}
	left = width.Clip(left, room)
	gap := cols - width.String(left) - rw
	if gap < 1 {
		gap = 1
	}
	return dim + left + reset + strings.Repeat(" ", gap) + right
}

func highlightRange(s string, anchor, pos int) string {
	if anchor > pos {
		anchor, pos = pos, anchor
	}
	if anchor < 0 {
		anchor = 0
	}
	if pos < len(s) {
		_, size := utf8.DecodeRuneInString(s[pos:])
		pos += size
	}
	if pos > len(s) {
		pos = len(s)
	}
	if anchor > len(s) {
		anchor = len(s)
	}
	if anchor >= pos {
		return blockAt(s, anchor)
	}
	return s[:anchor] + "\x1b[7m" + s[anchor:pos] + "\x1b[0m" + s[pos:]
}

func blockAt(s string, pos int) string {
	if pos > len(s) {
		pos = len(s)
	}
	if pos == len(s) {
		return s + "\x1b[7m \x1b[0m"
	}
	_, size := utf8.DecodeRuneInString(s[pos:])
	return s[:pos] + "\x1b[7m" + s[pos:pos+size] + "\x1b[0m" + s[pos+size:]
}

func (t *Transcript) syncCursor() {
	shape := "\x1b[0 q"
	if t.vim {
		if t.vimMode == "insert" {
			shape = "\x1b[6 q"
		} else {
			shape = "\x1b[2 q"
		}
	}
	if shape == t.cursorShape {
		return
	}
	t.cursorShape = shape
	_ = t.screen.WriteRaw(shape)
}

func (t *Transcript) hintRow() string {
	if t.ctrlC {
		return dim + width.Clip("press ctrl+c again to exit", t.cols) + reset
	}
	var left []string
	if !t.hasKey {
		left = append(left, "run /login")
	}
	mode := t.mode
	if mode == "" {
		mode = "auto"
	}
	if !t.hideSession {
		left = append(left, mode)
		if t.model != "" {
			left = append(left, t.model)
		}
	}
	if t.effort != "" {
		left = append(left, t.effort)
	}
	if !t.hideContext {
		if label := t.contextLabel(); label != "" && label != "0" {
			left = append(left, label)
		}
	}
	if !t.hideWorkspace && t.cwd != "" {
		left = append(left, filepathBase(t.cwd))
	}
	if t.fast {
		left = append(left, "\u26a1\ufe0e")
	}
	text := strings.Join(left, " · ")
	right := ""
	if t.vim {
		right = vimLabel(t.vimMode)
	} else if t.busy {
		right = "esc esc to interrupt"
	}
	if right == "" {
		return dim + width.Clip(text, t.cols) + reset
	}
	cols := t.cols - 1
	if cols < 1 {
		cols = 1
	}
	return justifyStatus(text, right, cols)
}

func tighten(s string) string {
	s = strings.TrimLeft(s, "\n")
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return s
}

func splitRow(line string) []string {
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "|") {
		line = line[1:]
	}
	if strings.HasSuffix(line, "|") && !strings.HasSuffix(line, "\\|") {
		line = line[:len(line)-1]
	}
	var cells []string
	var b strings.Builder
	code := false
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '`':
			code = !code
			b.WriteByte('`')
		case '\\':
			if !code && i+1 < len(line) {
				b.WriteByte(line[i+1])
				i++
				continue
			}
			b.WriteByte('\\')
		case '|':
			if code {
				b.WriteByte('|')
				continue
			}
			cells = append(cells, strings.TrimSpace(b.String()))
			b.Reset()
		default:
			b.WriteByte(line[i])
		}
	}
	cells = append(cells, strings.TrimSpace(b.String()))
	return cells
}

func renderTable(lines []string, cols int) []string {
	header := styleCells(splitRow(lines[0]))
	seps := splitRow(lines[1])
	var body [][]string
	for _, line := range lines[2:] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		body = append(body, styleCells(splitRow(line)))
	}
	n := len(header)
	if len(seps) > n {
		n = len(seps)
	}
	for _, row := range body {
		if len(row) > n {
			n = len(row)
		}
	}
	if n == 0 {
		return wrapPlain(indentText(strings.Join(lines, "\n")), cols)
	}
	for len(header) < n {
		header = append(header, "")
	}
	widths := make([]int, n)
	for i, cell := range header {
		if w := width.String(cell); w > widths[i] {
			widths[i] = w
		}
	}
	for _, row := range body {
		for i := 0; i < n && i < len(row); i++ {
			if w := width.String(row[i]); w > widths[i] {
				widths[i] = w
			}
		}
	}
	for i := range widths {
		if widths[i] < 1 {
			widths[i] = 1
		}
	}
	avail := cols - 3
	if avail < n {
		avail = n
	}
	fitWidths(widths, avail)
	aligns := make([]int, n)
	for i := 0; i < n && i < len(seps); i++ {
		aligns[i] = columnAlign(seps[i])
	}
	var out []string
	out = append(out, tableRow(header, widths, aligns, true)...)
	out = append(out, tableRule(widths))
	for _, row := range body {
		for len(row) < n {
			row = append(row, "")
		}
		out = append(out, tableRow(row, widths, aligns, false)...)
	}
	limit := cols - 1
	if limit < 1 {
		limit = 1
	}
	for i, row := range out {
		if width.String(row) > limit {
			out[i] = width.Clip(row, limit)
		}
	}
	return out
}

func styleCells(cells []string) []string {
	out := make([]string, len(cells))
	for i, cell := range cells {
		out[i] = styleInline(linkPaths(cell))
	}
	return out
}

func columnAlign(cell string) int {
	cell = strings.TrimSpace(cell)
	left := strings.HasPrefix(cell, ":")
	right := strings.HasSuffix(cell, ":")
	if left && right {
		return 2
	}
	if right {
		return 1
	}
	return 0
}

func fitWidths(widths []int, budget int) {
	sep := 0
	if len(widths) > 1 {
		sep = 3 * (len(widths) - 1)
	}
	if budget < len(widths)+sep {
		budget = len(widths) + sep
	}
	room := budget - sep
	for sumInts(widths) > room {
		i := 0
		for j, w := range widths {
			if w > widths[i] {
				i = j
			}
		}
		if widths[i] <= 1 {
			return
		}
		widths[i]--
	}
}

func sumInts(xs []int) int {
	n := 0
	for _, x := range xs {
		n += x
	}
	return n
}

func tableRow(cells []string, widths, aligns []int, header bool) []string {
	wrapped := make([][]string, len(widths))
	height := 1
	for i := 0; i < len(widths); i++ {
		text := ""
		if i < len(cells) {
			text = cells[i]
		}
		if header {
			text = bold + text + reset
		}
		lines := wrapStyled(text, widths[i])
		align := 0
		if !header && i < len(aligns) {
			align = aligns[i]
		}
		for li, line := range lines {
			lines[li] = padVisible(line, widths[i], align)
		}
		if len(lines) > height {
			height = len(lines)
		}
		wrapped[i] = lines
	}
	var out []string
	for h := 0; h < height; h++ {
		var b strings.Builder
		b.WriteString("  ")
		for i := 0; i < len(widths); i++ {
			if i > 0 {
				b.WriteString(" \u2502 ")
			}
			if h < len(wrapped[i]) {
				b.WriteString(wrapped[i][h])
			} else {
				b.WriteString(strings.Repeat(" ", widths[i]))
			}
		}
		out = append(out, b.String())
	}
	return out
}

func tableRule(widths []int) string {
	var b strings.Builder
	b.WriteString("  ")
	for i, w := range widths {
		if i > 0 {
			b.WriteString("\u2500\u253c\u2500")
		}
		if w < 1 {
			w = 1
		}
		b.WriteString(strings.Repeat("\u2500", w))
	}
	return dim + b.String() + reset
}

func padVisible(s string, cols, align int) string {
	gap := cols - width.String(s)
	if gap < 0 {
		gap = 0
	}
	switch align {
	case 1:
		return strings.Repeat(" ", gap) + s
	case 2:
		left := gap / 2
		return strings.Repeat(" ", left) + s + strings.Repeat(" ", gap-left)
	default:
		return s + strings.Repeat(" ", gap)
	}
}

func wrapStyled(s string, cols int) []string {
	if cols < 1 {
		cols = 1
	}
	if s == "" || width.String(s) <= cols {
		if s == "" {
			return []string{""}
		}
		return []string{s}
	}
	var lines []string
	var b strings.Builder
	open := ""
	col := 0
	flush := func() {
		if open != "" {
			b.WriteString(reset)
		}
		lines = append(lines, b.String())
		b.Reset()
		col = 0
		if open != "" {
			b.WriteString(open)
		}
	}
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			j := i + 1
			if j < len(s) && s[j] == '[' {
				j++
				for j < len(s) && !(s[j] >= 0x40 && s[j] <= 0x7e) {
					j++
				}
				if j < len(s) {
					j++
				}
			} else if j < len(s) {
				j++
			}
			seq := s[i:j]
			b.WriteString(seq)
			if seq == reset {
				open = ""
			} else if strings.HasSuffix(seq, "m") {
				open = seq
			}
			i = j
			continue
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		if size <= 0 {
			break
		}
		cw := width.String(s[i : i+size])
		if cw < 1 {
			cw = 1
		}
		if col > 0 && col+cw > cols {
			flush()
		}
		b.WriteString(s[i : i+size])
		col += cw
		i += size
	}
	if b.Len() > 0 || len(lines) == 0 {
		lines = append(lines, b.String())
	}
	return lines
}

func styleInline(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if strings.HasPrefix(s[i:], "**") || strings.HasPrefix(s[i:], "__") {
			mark := s[i : i+2]
			if end := strings.Index(s[i+2:], mark); end >= 0 {
				b.WriteString(bold)
				b.WriteString(s[i+2 : i+2+end])
				b.WriteString(reset)
				i += end + 4
				continue
			}
		}
		if s[i] == '`' {
			if end := strings.IndexByte(s[i+1:], '`'); end >= 0 {
				b.WriteString(dim)
				b.WriteString(s[i+1 : i+1+end])
				b.WriteString(reset)
				i += end + 2
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
func indentText(s string) string {
	if s == "" {
		return ""
	}
	parts := strings.Split(s, "\n")
	for i, part := range parts {
		if strings.TrimSpace(part) == "" {
			continue
		}
		if !strings.HasPrefix(part, "  ") {
			parts[i] = "  " + part
		}
	}
	return strings.Join(parts, "\n")
}

func (t *Transcript) toolTree() []string {
	if len(t.tools) == 0 {
		return nil
	}
	var rows []string
	rows = append(rows, "\u25cf "+toolHeadline(t.tools))
	for i, tool := range t.tools {
		branch := "\u251c "
		if i == len(t.tools)-1 {
			branch = "\u2514 "
		}
		rows = append(rows, branch+toolRow(tool))
	}
	return rows
}

func toolHeadline(tools []ToolLine) string {
	edits, cmds, reads, searches, fetches, agents, failed := 0, 0, 0, 0, 0, 0, 0
	for _, tool := range tools {
		if toolFailed(tool) {
			failed++
		}
		switch tool.Name {
		case "write_file", "edit_file":
			edits++
		case "shell":
			cmds++
		case "read_file":
			reads++
		case "grep_files", "glob_files":
			searches++
		case "web_fetch":
			fetches++
		case "subagent":
			agents++
		}
	}
	var parts []string
	parts = append(parts, countPhrase(edits, "edited a file", "edited files")...)
	parts = append(parts, countPhrase(cmds, "ran a command", "ran commands")...)
	parts = append(parts, countPhrase(reads, "read a file", "read files")...)
	parts = append(parts, countPhrase(searches, "searched", "searched")...)
	parts = append(parts, countPhrase(fetches, "fetched a page", "fetched pages")...)
	parts = append(parts, countPhrase(agents, "messaged a subagent", "messaged subagents")...)
	if len(parts) == 0 {
		parts = append(parts, "ran "+itoa(len(tools))+" "+plural(len(tools), "tool", "tools"))
	}
	parts[0] = strings.ToUpper(parts[0][:1]) + parts[0][1:]
	head := strings.Join(parts, ", ")
	if failed > 0 {
		head += " · " + itoa(failed) + " failed"
	}
	return head
}

func countPhrase(n int, one, many string) []string {
	if n == 0 {
		return nil
	}
	if n == 1 {
		return []string{one}
	}
	return []string{many}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func toolRow(tool ToolLine) string {
	if tool.Name == "shell" {
		return shellAction(tool)
	}
	label := toolAction(tool)
	if tool.Running || !toolFailed(tool) {
		return label
	}
	row := couldnt(tool)
	if reason := failureReason(tool.Output); reason != "" {
		row += " · " + reason
	}
	return width.Clip(row, 72)
}

func toolAction(tool ToolLine) string {
	switch tool.Name {
	case "read_file":
		return "Read " + readLabel(tool.Detail)
	case "grep_files", "glob_files":
		return "Searched " + width.Clip(searchTerms(tool.Detail), 64)
	case "web_fetch":
		url := jsonField(tool.Detail, "url")
		if url == "" {
			url = summaryText(tool.Detail)
		}
		if url == "" {
			return "Fetched a page"
		}
		return "Fetched " + width.Clip(url, 64)
	case "shell":
		return shellAction(tool)
	case "write_file", "edit_file":
		return "Edited " + editLabel(tool.Detail)
	case "subagent":
		return subagentLabel(tool)
	case "skill":
		name := jsonField(tool.Detail, "name")
		if name == "" {
			name = summaryText(tool.Detail)
		}
		if name == "" {
			return "Loaded a skill"
		}
		return "Loaded " + width.Clip(name, 64)
	default:
		extra := summaryText(tool.Detail)
		label := toolVerb(tool.Name)
		if extra == "" {
			return label
		}
		return width.Clip(label+" "+extra, 72)
	}
}

func shellAction(tool ToolLine) string {
	cmd := commandText(tool.Detail)
	if cmd == "" {
		cmd = "a command"
	}
	if code, ok := shellExit(tool.Output); ok && code != 0 {
		if code < 0 {
			return width.Clip("Timed out "+cmd, 72)
		}
		label := "Exited " + itoa(code) + " " + cmd
		if reason := failureReason(tool.Output); reason != "" {
			label += " · " + reason
		}
		return width.Clip(label, 72)
	}
	return "Ran " + width.Clip(cmd, 68)
}

func couldnt(tool ToolLine) string {
	switch tool.Name {
	case "read_file":
		return "Couldn't read " + readLabel(tool.Detail)
	case "grep_files", "glob_files":
		return "Couldn't search " + searchTerms(tool.Detail)
	case "web_fetch":
		url := jsonField(tool.Detail, "url")
		if url == "" {
			url = "a page"
		}
		return "Couldn't fetch " + url
	case "write_file", "edit_file":
		return "Couldn't edit " + editLabel(tool.Detail)
	case "subagent":
		who := jsonField(tool.Detail, "agent")
		if who == "" {
			who = "a subagent"
		}
		return "Couldn't message " + who
	default:
		return "Couldn't run " + toolVerb(tool.Name)
	}
}

func subagentLabel(tool ToolLine) string {
	action := jsonField(tool.Detail, "action")
	if action == "" {
		action = "message"
	}
	agent := jsonField(tool.Detail, "agent")
	switch action {
	case "wait":
		if tool.Running {
			return "waiting for subagents"
		}
		return "waited for subagents"
	case "status":
		target := agent
		if target == "" {
			target = "subagents"
		}
		if tool.Running {
			return "checking " + target
		}
		return "checked " + target
	case "stop":
		if agent == "" {
			agent = "a subagent"
		}
		if tool.Running {
			return "stopping " + agent
		}
		return "stopped " + agent
	case "close":
		if agent == "" {
			agent = "a subagent"
		}
		if tool.Running {
			return "closing " + agent
		}
		return "closed " + agent
	default:
		if jsonField(tool.Detail, "to") == "parent" {
			if tool.Running {
				return "sending to parent"
			}
			return "sent to parent"
		}
		if tool.Running {
			if agent == "" {
				return "starting a subagent"
			}
			return agent + " working"
		}
		if agent == "" {
			return "Messaged a subagent"
		}
		return "Messaged " + agent
	}
}

func toolVerb(name string) string {
	switch name {
	case "read_file":
		return "Read"
	case "grep_files", "glob_files":
		return "Searched"
	case "write_file", "edit_file":
		return "Edited"
	case "web_fetch":
		return "Fetched"
	case "shell":
		return "Ran"
	case "subagent":
		return "Messaged"
	default:
		if name == "" {
			return "Used"
		}
		return strings.ReplaceAll(name, "_", " ")
	}
}

func toolFailed(tool ToolLine) bool {
	if tool.Running {
		return false
	}
	out := strings.TrimSpace(tool.Output)
	if out == "" {
		return false
	}
	if tool.Name == "shell" {
		if code, ok := shellExit(out); ok {
			return code != 0
		}
	}
	if toolDump(out) {
		return false
	}
	low := strings.ToLower(out)
	if strings.HasPrefix(low, "permission denied") || strings.HasPrefix(low, "unknown tool") {
		return true
	}
	// A file listing is not a failure just because it contains "failed".
	// "error" and "fail" count only as their own word, so error_code does not.
	if strings.Contains(out, "\n") {
		return false
	}
	if hasOwnWord(low, "error") || hasOwnWord(low, "fail") || hasOwnWord(low, "failed") || hasOwnWord(low, "failure") {
		return true
	}
	return strings.Contains(low, "not found") || strings.Contains(low, "no such file") || strings.HasPrefix(low, "invalid ")
}

func hasOwnWord(s, word string) bool {
	if word == "" {
		return false
	}
	for i := 0; i+len(word) <= len(s); {
		j := strings.Index(s[i:], word)
		if j < 0 {
			return false
		}
		j += i
		left := j == 0 || !wordByte(s[j-1])
		right := j+len(word) == len(s) || !wordByte(s[j+len(word)])
		if left && right {
			return true
		}
		i = j + 1
	}
	return false
}

func wordByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

func shellExit(out string) (int, bool) {
	out = strings.TrimRight(out, "\n")
	line := out
	if i := strings.LastIndex(line, "\n"); i >= 0 {
		line = line[i+1:]
	}
	line = strings.TrimSpace(line)
	const key = "exit_code:"
	if !strings.HasPrefix(line, key) {
		return 0, false
	}
	rest := strings.TrimSpace(line[len(key):])
	if strings.HasPrefix(rest, "timeout") {
		return -1, true
	}
	n := 0
	ok := false
	for _, r := range rest {
		if r < '0' || r > '9' {
			break
		}
		ok = true
		n = n*10 + int(r-'0')
		if n > 999 {
			break
		}
	}
	return n, ok
}

func failureReason(out string) string {
	body := out
	if i := strings.Index(out, "<stderr>"); i >= 0 {
		body = out[i+len("<stderr>"):]
		if j := strings.Index(body, "</stderr>"); j >= 0 {
			body = body[:j]
		}
	} else if toolDump(out) {
		return ""
	}
	body = stripToolTags(body)
	var line string
	for _, part := range strings.Split(body, "\n") {
		part = strings.TrimSpace(part)
		if part == "" || strings.HasPrefix(part, "exit_code:") {
			continue
		}
		line = part
		break
	}
	line = oneLine(line)
	if line == "" || toolDump(line) {
		return ""
	}
	return line
}

func toolDump(s string) bool {
	return strings.Contains(s, "<content>") || strings.Contains(s, "</content>") || strings.Contains(s, "<stdout>") || strings.Contains(s, "<stderr>") || strings.Contains(s, "<path>")
}

func stripToolTags(s string) string {
	for _, tag := range []string{"<stdout>", "</stdout>", "<stderr>", "</stderr>", "<content>", "</content>", "<path>", "</path>"} {
		s = strings.ReplaceAll(s, tag, "")
	}
	return s
}

func toolSummary(tools []ToolLine, d time.Duration) string {
	files := 0
	for _, tool := range tools {
		if tool.Name == "read_file" || tool.Name == "grep_files" || tool.Name == "glob_files" {
			files++
		}
	}
	what := "Ran " + itoa(len(tools)) + " " + plural(len(tools), "tool", "tools")
	if files > 0 {
		what = "Explored " + itoa(files) + " files"
	}
	sec := int(d.Round(time.Second) / time.Second)
	if sec < 1 {
		sec = 1
	}
	return what + " · " + itoa(sec) + "s"
}

func commandText(detail string) string {
	if cmd := jsonField(detail, "command"); cmd != "" {
		return oneLine(cmd)
	}
	if id := jsonField(detail, "session_id"); id != "" {
		action := jsonField(detail, "action")
		if action == "" {
			action = "session"
		}
		return action + " " + id
	}
	return summaryText(detail)
}

func editLabel(detail string) string {
	path := jsonField(detail, "path")
	if path == "" {
		return "a file"
	}
	return fileLink(filepathBase(path), path)
}

func summaryText(detail string) string {
	detail = strings.TrimSpace(detail)
	if strings.HasPrefix(detail, "{") {
		for _, key := range []string{"command", "path", "pattern", "url", "query", "name", "agent"} {
			if value := jsonField(detail, key); value != "" {
				return oneLine(value)
			}
		}
		return ""
	}
	if toolDump(detail) {
		return ""
	}
	return oneLine(detail)
}

func visibleDetail(name, detail string) string {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return ""
	}
	if name == "subagent" {
		return subagentLabel(ToolLine{Name: name, Detail: detail, Running: true})
	}
	if name == "shell" {
		if cmd := commandText(detail); cmd != "" {
			return cmd
		}
	}
	if v := summaryText(detail); v != "" {
		return v
	}
	if strings.HasPrefix(detail, "{") {
		return name
	}
	return oneLine(detail)
}

func linkPaths(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '`' {
			b.WriteByte(s[i])
			i++
			continue
		}
		j := strings.IndexByte(s[i+1:], '`')
		if j < 0 {
			b.WriteByte(s[i])
			i++
			continue
		}
		inner := s[i+1 : i+1+j]
		if path, ok := codePath(inner); ok {
			b.WriteString(fileLink(filepathBase(path), path))
			i += j + 2
			continue
		}
		b.WriteString(s[i : i+j+2])
		i += j + 2
	}
	return b.String()
}

func codePath(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, " \n\t") {
		return "", false
	}
	if strings.HasPrefix(s, "/") || strings.HasPrefix(s, "~/") {
		return s, true
	}
	return "", false
}

func fileLink(label, path string) string {
	if path == "" {
		return label
	}
	return "\x1b]8;;" + fileURL(path) + "\x1b\\" + label + "\x1b]8;;\x1b\\"
}

func readLabel(detail string) string {
	path := jsonField(detail, "path")
	if path == "" {
		path = summaryText(detail)
	}
	if path == "" {
		return "a file"
	}
	start := jsonField(detail, "start_line")
	count := jsonField(detail, "line_count")
	label := fileLink(filepathBase(path), path)
	if start != "" {
		label += " L" + start
		if count != "" {
			label += "-" + count
		}
	}
	return label
}

func searchTerms(detail string) string {
	if p := jsonField(detail, "pattern"); p != "" {
		return p
	}
	if strings.HasPrefix(strings.TrimSpace(detail), "{") {
		return "files"
	}
	if s := summaryText(detail); s != "" {
		return s
	}
	return "files"
}

func jsonField(raw, key string) string {
	var m map[string]any
	if json.Unmarshal([]byte(raw), &m) != nil {
		return ""
	}
	switch v := m[key].(type) {
	case string:
		return v
	case float64:
		return itoa(int(v))
	default:
		return ""
	}
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

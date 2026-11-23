package tui

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Composer is the input line. Ctrl+C arms an exit. A second Ctrl+C exits.
// Escape twice interrupts the running turn. Vim, when enabled, uses insert,
// normal, and visual modes.
type Composer struct {
	Line    string
	Pos     int
	Armed   bool
	Esc     bool
	Vim     bool
	Mode    string
	Anchor  int
	editing bool
	undo    []edit
	redo    []edit
	History []string
	hist    int
	pending []byte
}

// PendingEsc reports a lone escape waiting to see if a cursor sequence follows.
func (c *Composer) PendingEsc() bool {
	return c != nil && len(c.pending) == 1 && c.pending[0] == 0x1b
}

// FinishEsc treats a lone escape as complete. In vim mode that enters normal.
func (c *Composer) FinishEsc() Action {
	if !c.PendingEsc() {
		return Action{}
	}
	c.pending = nil
	return c.bareEsc()
}

// Action is what Feed decided.
type Action struct {
	Submit    string
	Exit      bool
	Interrupt bool
	Redraw    bool
	Detail    bool
	Up        bool
	Down      bool
	Tab       bool
	Close     bool
}

// Feed consumes one chunk of terminal input.
func (c *Composer) Feed(b []byte) Action {
	if len(c.pending) > 0 {
		b = append(append([]byte{}, c.pending...), b...)
		c.pending = nil
	}
	var act Action
	for len(b) > 0 {
		switch b[0] {
		case 3: // ctrl+c
			b = b[1:]
			if c.Armed {
				act.Exit = true
				return act
			}
			c.Armed = true
			act.Redraw = true
		case 15: // ctrl+o
			b = b[1:]
			c.Armed = false
			act.Detail = true
			act.Redraw = true
		case 4: // ctrl+d
			b = b[1:]
			if c.Line == "" {
				act.Exit = true
				return act
			}
			c.deleteForward()
			act.Redraw = true
		case '\r', '\n':
			b = b[1:]
			c.Armed = false
			c.Esc = false
			act.Submit = c.Line
			if c.Line != "" {
				c.History = append(c.History, c.Line)
			}
			c.hist = len(c.History)
			c.Line = ""
			c.Pos = 0
			act.Redraw = true
			return act
		case 127, 8:
			b = b[1:]
			c.disarm()
			c.deleteBack()
			act.Redraw = true
		case 1:
			b = b[1:]
			c.disarm()
			c.Pos = 0
			act.Redraw = true
		case 5:
			b = b[1:]
			c.disarm()
			c.Pos = len(c.Line)
			act.Redraw = true
		case 11:
			b = b[1:]
			c.disarm()
			c.Line = c.Line[:c.Pos]
			act.Redraw = true
		case 21:
			b = b[1:]
			c.disarm()
			c.Line = c.Line[c.Pos:]
			c.Pos = 0
			act.Redraw = true
		case 23: // ctrl+w
			b = b[1:]
			c.disarm()
			c.deleteWordBack()
			act.Redraw = true
		case 18: // ctrl+r
			b = b[1:]
			if c.commandMode() {
				c.redoEdit()
				act.Redraw = true
			}
		case '\t':
			b = b[1:]
			c.disarm()
			act.Tab = true
			act.Redraw = true
		case 0x1b:
			if len(b) >= 2 && (b[1] == 127 || b[1] == 8) {
				b = b[2:]
				c.disarm()
				c.deleteWordBack()
				act.Redraw = true
				continue
			}
			if len(b) == 1 {
				c.pending = append([]byte{}, b...)
				return act
			}
			rest, a := c.escape(b)
			b = rest
			act.Interrupt = act.Interrupt || a.Interrupt
			act.Up = act.Up || a.Up
			act.Down = act.Down || a.Down
			act.Close = act.Close || a.Close
			act.Redraw = true
		default:
			if b[0] < 0x20 {
				b = b[1:]
				continue
			}
			c.disarm()
			if !utf8.FullRune(b) && len(b) < utf8.UTFMax {
				c.pending = append([]byte{}, b...)
				return act
			}
			r, size := decode(b)
			b = b[size:]
			if c.commandMode() {
				c.vimKey(r)
			} else {
				c.insert(r)
			}
			act.Redraw = true
		}
	}
	return act
}

func (c *Composer) disarm() {
	c.Armed = false
	c.Esc = false
}

func (c *Composer) deleteBack() {
	if c.Pos == 0 {
		return
	}
	c.beforeChange()
	_, size := decodeLast(c.Line[:c.Pos])
	c.Line = c.Line[:c.Pos-size] + c.Line[c.Pos:]
	c.Pos -= size
}

func (c *Composer) deleteForward() {
	if c.Pos >= len(c.Line) {
		return
	}
	c.beforeChange()
	_, size := decode([]byte(c.Line[c.Pos:]))
	c.Line = c.Line[:c.Pos] + c.Line[c.Pos+size:]
}

func (c *Composer) escape(b []byte) ([]byte, Action) {
	if len(b) == 1 {
		if strings.HasPrefix(strings.TrimSpace(c.Line), "/") {
			return nil, Action{Close: true}
		}
		if c.Esc {
			c.Esc = false
			return nil, Action{Interrupt: true}
		}
		c.Esc = true
		c.Armed = false
		return nil, Action{}
	}
	c.Esc = false
	c.Armed = false
	if len(b) >= 3 && (b[1] == '[' || b[1] == 'O') {
		switch b[2] {
		case 'A':
			if strings.HasPrefix(strings.TrimSpace(c.Line), "/") {
				return b[3:], Action{Up: true, Redraw: true}
			}
			c.history(-1)
			return b[3:], Action{}
		case 'B':
			if strings.HasPrefix(strings.TrimSpace(c.Line), "/") {
				return b[3:], Action{Down: true, Redraw: true}
			}
			c.history(1)
			return b[3:], Action{}
		case 'C':
			c.move(1)
			return b[3:], Action{}
		case 'D':
			c.move(-1)
			return b[3:], Action{}
		case 'H':
			c.Pos = 0
			return b[3:], Action{}
		case 'F':
			c.Pos = len(c.Line)
			return b[3:], Action{}
		}
	}
	if strings.HasPrefix(strings.TrimSpace(c.Line), "/") {
		return b[1:], Action{Close: true}
	}
	if c.Vim {
		c.enterNormal()
		return b[1:], Action{}
	}
	return b[1:], Action{}
}

func (c *Composer) bareEsc() Action {
	if strings.HasPrefix(strings.TrimSpace(c.Line), "/") {
		return Action{Close: true, Redraw: true}
	}
	if c.Vim {
		c.enterNormal()
		return Action{Redraw: true}
	}
	if c.Esc {
		c.Esc = false
		return Action{Interrupt: true, Redraw: true}
	}
	c.Esc = true
	c.Armed = false
	return Action{Redraw: true}
}

func (c *Composer) commandMode() bool {
	return c.Vim && (c.Mode == "normal" || c.Mode == "visual")
}

func (c *Composer) enterNormal() {
	c.Mode = "normal"
	c.editing = false
	if c.Pos > 0 && c.Pos >= len(c.Line) {
		c.move(-1)
	}
}

func (c *Composer) insert(r rune) {
	c.beforeChange()
	s := string(r)
	c.Line = c.Line[:c.Pos] + s + c.Line[c.Pos:]
	c.Pos += len(s)
	c.Mode = "insert"
}

func (c *Composer) vimKey(r rune) {
	switch r {
	case 'h':
		c.move(-1)
	case 'l':
		c.move(1)
	case '0':
		c.Pos = 0
	case '$':
		c.Pos = len(c.Line)
		if c.Mode == "normal" && c.Pos > 0 {
			c.move(-1)
		}
	case 'i':
		if c.Mode == "visual" {
			return
		}
		c.Mode = "insert"
	case 'a':
		if c.Mode == "visual" {
			return
		}
		c.move(1)
		c.Mode = "insert"
	case 'I':
		c.Pos = 0
		c.Mode = "insert"
	case 'A':
		c.Pos = len(c.Line)
		c.Mode = "insert"
	case 'u':
		c.undoEdit()
	case 'x':
		if c.Mode == "visual" {
			c.deleteSelection()
			return
		}
		c.deleteForward()
	case 'X':
		c.deleteBack()
	case 'd', 'D':
		if c.Mode == "visual" || r == 'D' {
			if r == 'D' && c.Mode != "visual" {
				c.beforeChange()
				c.Line = c.Line[:c.Pos]
				return
			}
			c.deleteSelection()
			return
		}
		c.beforeChange()
		c.Line = ""
		c.Pos = 0
	case 'v':
		if c.Mode == "visual" {
			c.Mode = "normal"
			return
		}
		c.Mode = "visual"
		c.Anchor = c.Pos
	case 'w':
		c.word(1)
	case 'b':
		c.word(-1)
	default:
		if c.Mode == "normal" && r == '/' {
			c.Mode = "insert"
			c.insert('/')
		}
	}
}

type edit struct {
	line string
	pos  int
}

func (c *Composer) beforeChange() {
	if !c.Vim {
		return
	}
	if c.Mode == "insert" || c.Mode == "" {
		if c.editing {
			return
		}
		c.remember()
		c.editing = true
		return
	}
	c.remember()
}

func (c *Composer) remember() {
	c.undo = append(c.undo, edit{line: c.Line, pos: c.Pos})
	c.redo = nil
	if len(c.undo) > 100 {
		c.undo = c.undo[1:]
	}
}

func (c *Composer) undoEdit() {
	if len(c.undo) == 0 {
		return
	}
	c.redo = append(c.redo, edit{line: c.Line, pos: c.Pos})
	last := c.undo[len(c.undo)-1]
	c.undo = c.undo[:len(c.undo)-1]
	c.Line = last.line
	c.Pos = last.pos
	c.editing = false
	c.Mode = "normal"
}

func (c *Composer) redoEdit() {
	if len(c.redo) == 0 {
		return
	}
	c.undo = append(c.undo, edit{line: c.Line, pos: c.Pos})
	next := c.redo[len(c.redo)-1]
	c.redo = c.redo[:len(c.redo)-1]
	c.Line = next.line
	c.Pos = next.pos
	c.editing = false
	c.Mode = "normal"
}

func (c *Composer) deleteSelection() {
	c.beforeChange()
	a, b := c.Anchor, c.Pos
	if a > b {
		a, b = b, a
	}
	if b < len(c.Line) {
		_, size := decode([]byte(c.Line[b:]))
		b += size
	}
	if a < 0 {
		a = 0
	}
	if b > len(c.Line) {
		b = len(c.Line)
	}
	c.Line = c.Line[:a] + c.Line[b:]
	c.Pos = a
	c.Mode = "normal"
}

func (c *Composer) deleteWordBack() {
	if c.Pos == 0 {
		return
	}
	c.beforeChange()
	i := c.Pos
	for i > 0 {
		r, size := decodeLast(c.Line[:i])
		if !unicode.IsSpace(r) {
			break
		}
		i -= size
	}
	for i > 0 {
		r, size := decodeLast(c.Line[:i])
		if unicode.IsSpace(r) {
			break
		}
		i -= size
	}
	c.Line = c.Line[:i] + c.Line[c.Pos:]
	c.Pos = i
}

func (c *Composer) word(dir int) {
	if dir < 0 {
		i := c.Pos
		for i > 0 {
			r, size := decodeLast(c.Line[:i])
			if !unicode.IsSpace(r) {
				break
			}
			i -= size
		}
		for i > 0 {
			r, size := decodeLast(c.Line[:i])
			if unicode.IsSpace(r) {
				break
			}
			i -= size
		}
		c.Pos = i
		return
	}
	i := c.Pos
	for i < len(c.Line) {
		r, size := decode([]byte(c.Line[i:]))
		if unicode.IsSpace(r) {
			break
		}
		i += size
	}
	for i < len(c.Line) {
		r, size := decode([]byte(c.Line[i:]))
		if !unicode.IsSpace(r) {
			break
		}
		i += size
	}
	c.Pos = i
}

func (c *Composer) history(dir int) {
	if len(c.History) == 0 {
		return
	}
	c.hist += dir
	if c.hist < 0 {
		c.hist = 0
	}
	if c.hist >= len(c.History) {
		c.hist = len(c.History)
		c.Line = ""
		c.Pos = 0
		return
	}
	c.Line = c.History[c.hist]
	c.Pos = len(c.Line)
}

func (c *Composer) move(dir int) {
	if dir < 0 {
		if c.Pos == 0 {
			return
		}
		_, size := decodeLast(c.Line[:c.Pos])
		c.Pos -= size
		return
	}
	if c.Pos >= len(c.Line) {
		return
	}
	_, size := decode([]byte(c.Line[c.Pos:]))
	c.Pos += size
}

func decode(b []byte) (rune, int) {
	if len(b) == 0 {
		return 0, 0
	}
	return utf8.DecodeRune(b)
}

func decodeLast(s string) (rune, int) {
	if s == "" {
		return 0, 0
	}
	return utf8.DecodeLastRuneInString(s)
}

// Hint is the footer text for an armed Ctrl+C.
func Hint(armed bool) string {
	if armed {
		return "press ctrl+c again to exit"
	}
	return ""
}

func (c *Composer) String() string { return strings.TrimRight(c.Line, "\n") }

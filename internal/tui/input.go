package tui

import (
	"strings"
	"unicode/utf8"
)

// Composer is the input line. Ctrl+C arms an exit. A second Ctrl+C exits.
// Escape twice interrupts the running turn.
type Composer struct {
	Line    string
	Pos     int
	Armed   bool
	Esc     bool
	History []string
	hist    int
	pending []byte
}

// Action is what Feed decided.
type Action struct {
	Submit    string
	Exit      bool
	Interrupt bool
	Redraw    bool
	Detail    bool
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
		case 0x1b:
			rest, a := c.escape(b)
			b = rest
			if a.Interrupt {
				act.Interrupt = true
			}
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
			c.Line = c.Line[:c.Pos] + string(r) + c.Line[c.Pos:]
			c.Pos += len(string(r))
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
	_, size := decodeLast(c.Line[:c.Pos])
	c.Line = c.Line[:c.Pos-size] + c.Line[c.Pos:]
	c.Pos -= size
}

func (c *Composer) deleteForward() {
	if c.Pos >= len(c.Line) {
		return
	}
	_, size := decode([]byte(c.Line[c.Pos:]))
	c.Line = c.Line[:c.Pos] + c.Line[c.Pos+size:]
}

func (c *Composer) escape(b []byte) ([]byte, Action) {
	if len(b) == 1 {
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
	if len(b) >= 3 && b[1] == '[' {
		switch b[2] {
		case 'A':
			c.history(-1)
			return b[3:], Action{}
		case 'B':
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
	return b[1:], Action{}
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
		c.deleteBack()
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

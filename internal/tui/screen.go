package tui

import (
	"fmt"
	"io"
	"strings"
)

// Screen paints a live frame under scrollback. Committed lines are written
// once. Later paints rewrite only the frame, from the row the cursor was
// left on. Newlines are not used inside the frame: a newline on the last
// screen row scrolls, which stacks a new prompt on every keystroke.
type Screen struct {
	out       io.Writer
	height    int
	cursorRow int
	prev      []string
}

func NewScreen(out io.Writer) *Screen { return &Screen{out: out} }

// Release drops the first n frame rows without erasing them, so they stay
// in scrollback above the footer.
func (s *Screen) Release(n int) {
	if n <= 0 || s.height == 0 {
		return
	}
	if n > s.height {
		n = s.height
	}
	if n > len(s.prev) {
		n = len(s.prev)
	}
	s.prev = append([]string(nil), s.prev[n:]...)
	s.height -= n
	s.cursorRow -= n
	if s.cursorRow < 0 {
		s.cursorRow = 0
	}
}

// Commit writes lines into scrollback and drops the frame.
func (s *Screen) Commit(lines []string) error {
	if len(lines) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("\x1b[?2026h")
	s.moveHome(&b)
	for _, line := range lines {
		b.WriteString(line)
		b.WriteString("\x1b[K\r\n")
	}
	if extra := s.height - len(lines); extra > 0 {
		fmt.Fprintf(&b, "\x1b[%dM", extra)
	}
	b.WriteString("\x1b[?2026l")
	s.height = 0
	s.cursorRow = 0
	s.prev = nil
	_, err := io.WriteString(s.out, b.String())
	return err
}

func (s *Screen) moveHome(b *strings.Builder) {
	if s.cursorRow > 0 {
		fmt.Fprintf(b, "\x1b[%dA", s.cursorRow)
	}
	if s.height > 0 || s.cursorRow > 0 {
		b.WriteByte('\r')
	}
}

// Frame is the live rows and where the cursor sits inside them.
type Frame struct {
	Rows      []string
	CursorRow int
	CursorCol int
}

// Paint diffs the frame. Unchanged rows are not rewritten. Movement inside
// the frame uses cursor up/down, never a newline, so the last screen row
// cannot scroll a new prompt into place.
func (s *Screen) Paint(frame Frame) error {
	rows := frame.Rows
	n := len(rows)
	var b strings.Builder
	b.WriteString("\x1b[?2026h")
	if s.height == 0 {
		if n > 1 {
			for i := 1; i < n; i++ {
				b.WriteString("\r\n")
			}
			fmt.Fprintf(&b, "\x1b[%dA", n-1)
		}
		b.WriteByte('\r')
	} else {
		s.moveHome(&b)
		if n > s.height {
			if s.height > 1 {
				fmt.Fprintf(&b, "\x1b[%dB", s.height-1)
			}
			for i := s.height; i < n; i++ {
				b.WriteString("\r\n")
			}
			if n > 1 {
				fmt.Fprintf(&b, "\x1b[%dA", n-1)
			}
			b.WriteByte('\r')
		}
	}
	limit := n
	if s.height > limit {
		limit = s.height
	}
	for i := 0; i < limit; i++ {
		if i > 0 {
			b.WriteString("\x1b[1B\r")
		}
		if i < n {
			if i >= len(s.prev) || s.prev[i] != rows[i] {
				b.WriteString(rows[i])
				b.WriteString("\x1b[K")
			}
		} else {
			b.WriteString("\x1b[K")
		}
	}
	dest := frame.CursorRow
	if n == 0 {
		dest = 0
	} else if dest < 0 {
		dest = 0
	} else if dest >= n {
		dest = n - 1
	}
	from := 0
	if limit > 0 {
		from = limit - 1
	}
	if from > dest {
		fmt.Fprintf(&b, "\x1b[%dA", from-dest)
	} else if dest > from {
		fmt.Fprintf(&b, "\x1b[%dB", dest-from)
	}
	col := frame.CursorCol
	if col < 1 {
		col = 1
	}
	if n > 0 {
		fmt.Fprintf(&b, "\x1b[%dG", col)
	}
	b.WriteString("\x1b[?2026l")
	s.prev = append(s.prev[:0], rows...)
	s.height = n
	s.cursorRow = dest
	_, err := io.WriteString(s.out, b.String())
	return err
}

// Reset forgets the live frame. The next paint starts from a clear cursor.
func (s *Screen) Reset() {
	s.height = 0
	s.cursorRow = 0
	s.prev = nil
}

// WriteRaw writes bytes without updating the frame.
func (s *Screen) WriteRaw(text string) error {
	_, err := io.WriteString(s.out, text)
	return err
}

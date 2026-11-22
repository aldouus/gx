package tui

import (
	"fmt"
	"io"
	"strings"
)

// Screen paints a live frame under scrollback. Committed lines are written
// once. Later paints rewrite only the frame.
type Screen struct {
	out    io.Writer
	height int
	prev   []string
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
}

// Commit writes lines into scrollback and drops the frame.
func (s *Screen) Commit(lines []string) error {
	if len(lines) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("\x1b[?2026h")
	if s.height > 1 {
		fmt.Fprintf(&b, "\x1b[%dA", s.height-1)
	}
	if s.height > 0 {
		b.WriteByte('\r')
	}
	for _, line := range lines {
		b.WriteString(line)
		b.WriteString("\x1b[K\r\n")
	}
	if extra := s.height - len(lines); extra > 0 {
		for i := 0; i < extra; i++ {
			b.WriteString("\x1b[K")
			if i+1 < extra {
				b.WriteString("\x1b[1E")
			}
		}
		if extra > 1 {
			fmt.Fprintf(&b, "\x1b[%dA", extra-1)
		}
		b.WriteByte('\r')
	}
	b.WriteString("\x1b[?2026l")
	s.height = 0
	s.prev = nil
	_, err := io.WriteString(s.out, b.String())
	return err
}

// Frame is the live rows and where the cursor sits inside them.
type Frame struct {
	Rows      []string
	CursorRow int
	CursorCol int
}

// Paint diffs the frame. Unchanged rows are not rewritten.
func (s *Screen) Paint(frame Frame) error {
	rows := frame.Rows
	var b strings.Builder
	b.WriteString("\x1b[?2026h")
	if s.height > 1 {
		fmt.Fprintf(&b, "\x1b[%dA", s.height-1)
	}
	if s.height > 0 {
		b.WriteByte('\r')
	}
	n := len(rows)
	limit := n
	if s.height > limit {
		limit = s.height
	}
	for i := 0; i < limit; i++ {
		if i > 0 {
			b.WriteString("\x1b[1E")
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
	if n > 0 && n < s.height {
		up := s.height - n
		if limit-1 > n-1 {
			fmt.Fprintf(&b, "\x1b[%dA", limit-n)
		}
		_ = up
	}
	if n > 0 {
		// Cursor is on the last painted row. Move to the composer.
		from := limit - 1
		if n < s.height {
			from = n - 1
		}
		if frame.CursorRow >= 0 && frame.CursorRow < n && from != frame.CursorRow {
			if from > frame.CursorRow {
				fmt.Fprintf(&b, "\x1b[%dA", from-frame.CursorRow)
			} else {
				fmt.Fprintf(&b, "\x1b[%dB", frame.CursorRow-from)
			}
		}
		col := frame.CursorCol
		if col < 1 {
			col = 1
		}
		fmt.Fprintf(&b, "\x1b[%dG", col)
	}
	b.WriteString("\x1b[?2026l")
	s.prev = append(s.prev[:0], rows...)
	s.height = n
	_, err := io.WriteString(s.out, b.String())
	return err
}

// Reset forgets the live frame. The next paint starts from a clear cursor.
func (s *Screen) Reset() {
	s.height = 0
	s.prev = nil
}

// WriteRaw writes bytes without updating the frame.
func (s *Screen) WriteRaw(text string) error {
	_, err := io.WriteString(s.out, text)
	return err
}

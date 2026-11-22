// Package width measures terminal cells. The standard library has no
// display width, so the wide-character ranges live in this package.
package width

import (
	"sort"
	"unicode"
	"unicode/utf8"
)

// wideRanges are Unicode East Asian Width Wide and Fullwidth blocks,
// plus emoji presentation blocks the footer clips as two cells.
var wideRanges = [][2]rune{
	{0x1100, 0x115F},
	{0x2329, 0x232A},
	{0x2E80, 0x303E},
	{0x3040, 0xA4CF},
	{0xAC00, 0xD7A3},
	{0xF900, 0xFAFF},
	{0xFE10, 0xFE19},
	{0xFE30, 0xFE6F},
	{0xFF00, 0xFF60},
	{0xFFE0, 0xFFE6},
	{0x1F300, 0x1F64F},
	{0x1F680, 0x1F6FF},
	{0x1F900, 0x1FAFF},
	{0x20000, 0x3FFFD},
}

// Rune is the cell width of one scalar: 0, 1, or 2.
func Rune(r rune) int {
	if r == 0 || r < 0x20 || (r >= 0x7F && r < 0xA0) {
		return 0
	}
	if r == 0x200D || (r >= 0x1F3FB && r <= 0x1F3FF) || (r >= 0xFE00 && r <= 0xFE0F) {
		return 0
	}
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || unicode.Is(unicode.Cf, r) {
		return 0
	}
	if inRanges(r, wideRanges) {
		return 2
	}
	return 1
}

func inRanges(r rune, ranges [][2]rune) bool {
	i := sort.Search(len(ranges), func(i int) bool { return ranges[i][1] >= r })
	if i >= len(ranges) {
		return false
	}
	return r >= ranges[i][0] && r <= ranges[i][1]
}

// String is the cell width of s. ANSI sequences count as zero.
func String(s string) int { return measure(s, true) }

// Visible is the cell width of s with no ANSI skipping.
func Visible(s string) int { return measure(s, false) }

func measure(s string, skipANSI bool) int {
	w := 0
	for i := 0; i < len(s); {
		if skipANSI && s[i] == 0x1b {
			i = ansiEnd(s, i)
			continue
		}
		n, cw := unitAt(s, i)
		if n == 0 {
			break
		}
		w += cw
		i += n
	}
	return w
}

func unitAt(s string, i int) (byteLen, cells int) {
	if i >= len(s) {
		return 0, 0
	}
	r, size := utf8.DecodeRuneInString(s[i:])
	if r == utf8.RuneError && size == 1 {
		return 1, 1
	}
	if i+size < len(s) {
		next, nsize := utf8.DecodeRuneInString(s[i+size:])
		if next == 0xFE0F {
			return size + nsize, 2
		}
		if next == 0xFE0E {
			cw := 1
			if inRanges(r, wideRanges) {
				cw = 2
			}
			return size + nsize, cw
		}
	}
	return size, Rune(r)
}

// Prefix returns the longest prefix of s whose cell width is at most cols.
// ANSI sequences are kept and do not consume columns.
func Prefix(s string, cols int) string {
	if cols <= 0 {
		return ""
	}
	w := 0
	i := 0
	for i < len(s) {
		if s[i] == 0x1b {
			i = ansiEnd(s, i)
			continue
		}
		n, cw := unitAt(s, i)
		if n == 0 || w+cw > cols {
			break
		}
		w += cw
		i += n
	}
	return s[:i]
}

// Suffix returns the longest suffix of s whose cell width is at most cols.
func Suffix(s string, cols int) string {
	if cols <= 0 {
		return ""
	}
	if String(s) <= cols {
		return s
	}
	w := 0
	i := len(s)
	for i > 0 {
		if s[i-1] == 0x1b || (i >= 2 && s[i-1] >= 0x40 && s[i-1] <= 0x7e && hasESC(s[:i])) {
			// Walk back one rune; ANSI is skipped by measuring forward from a cut.
			break
		}
		_, size := utf8.DecodeLastRuneInString(s[:i])
		if size <= 0 {
			break
		}
		cw := String(s[i-size : i])
		if w+cw > cols {
			break
		}
		w += cw
		i -= size
	}
	return s[i:]
}

func hasESC(s string) bool {
	return len(s) > 0 && s[0] == 0x1b
}

// Clip shortens s to cols, ending with an ellipsis when it does not fit.
func Clip(s string, cols int) string {
	if cols <= 0 {
		return ""
	}
	if String(s) <= cols {
		return s
	}
	if cols == 1 {
		return "…"
	}
	return Prefix(s, cols-1) + "…"
}

func ansiEnd(s string, i int) int {
	if i >= len(s) || s[i] != 0x1b {
		return i
	}
	if i+1 >= len(s) {
		return len(s)
	}
	switch s[i+1] {
	case '[':
		j := i + 2
		for j < len(s) {
			if s[j] >= 0x40 && s[j] <= 0x7e {
				return j + 1
			}
			j++
		}
		return len(s)
	case ']':
		j := i + 2
		for j < len(s) {
			if s[j] == 0x07 {
				return j + 1
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				return j + 2
			}
			j++
		}
		return len(s)
	default:
		if i+2 <= len(s) {
			return i + 2
		}
		return len(s)
	}
}

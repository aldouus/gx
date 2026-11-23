package tui

import (
	"io"
	"unicode/utf8"
)

// DecidePermission reads one permission decision.
// Arrow keys move the selection. 1, 2, and 3 select only while the note is
// empty. Enter submits. y allows once and n denies until typing has started.
// Tab opens the note for the yes or no choice. Esc and ctrl+c deny.
func DecidePermission(in io.Reader, paint func(choice int, note string, amend bool) error) (int, string, error) {
	choice := 0
	var note []rune
	amend := false
	sawEsc := false
	var pending []byte
	buf := make([]byte, 32)
	paintNow := func() {
		if paint != nil {
			_ = paint(choice, string(note), amend)
		}
	}
	for {
		n, err := in.Read(buf)
		if err != nil {
			return 0, "", err
		}
		chunk := buf[:n]
		if sawEsc {
			sawEsc = false
			if len(chunk) > 0 && (chunk[0] == '[' || chunk[0] == 'O') {
				chunk = append([]byte{0x1b}, chunk...)
			} else {
				return 2, string(note), nil
			}
		}
		pending = append(pending, chunk...)
		for len(pending) > 0 {
			b := pending[0]
			if b == 0x1b {
				if len(pending) == 1 {
					sawEsc = true
					pending = pending[1:]
					break
				}
				if pending[1] == '[' || pending[1] == 'O' {
					if len(pending) < 3 {
						break
					}
					switch pending[2] {
					case 'A':
						if choice > 0 {
							choice--
						}
						amend = false
					case 'B':
						if choice < 2 {
							choice++
						}
						amend = false
					}
					pending = pending[3:]
					paintNow()
					continue
				}
				return 2, string(note), nil
			}
			pending = pending[1:]
			switch b {
			case '\t':
				if amend {
					break
				}
				if choice == 0 || choice == 2 {
					amend = true
				} else if choice < 2 {
					choice++
				}
			case '1', '2', '3':
				if len(note) == 0 {
					choice = int(b - '1')
					amend = false
					break
				}
				note = append(note, rune(b))
				amend = true
			case 'y', 'Y':
				if len(note) == 0 && !amend {
					return 0, "", nil
				}
				note = append(note, rune(b))
				amend = true
			case 'n', 'N':
				if len(note) == 0 && !amend {
					return 2, "", nil
				}
				note = append(note, rune(b))
				amend = true
			case 3:
				return 2, string(note), nil
			case '\r', '\n':
				return choice, string(note), nil
			case 127, 8:
				if len(note) > 0 {
					note = note[:len(note)-1]
				}
			default:
				if b < 32 {
					break
				}
				rest := append([]byte{b}, pending...)
				if !utf8.FullRune(rest) && len(rest) < utf8.UTFMax {
					pending = rest
					paintNow()
					goto more
				}
				r, size := utf8.DecodeRune(rest)
				if r == utf8.RuneError && size == 1 {
					break
				}
				pending = rest[size:]
				note = append(note, r)
				amend = true
			}
			paintNow()
		}
	more:
	}
}

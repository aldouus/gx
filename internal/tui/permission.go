package tui

import (
	"io"
	"unicode/utf8"
)

// DecidePermission reads one permission decision.
// 1, 2, and 3 select allow once, allow always, and deny. Enter submits the
// selection. y submits allow once and n, esc, and ctrl+c submit deny.
// Other characters are a note returned with the decision.
func DecidePermission(in io.Reader, paint func(choice int, note string) error) (int, string, error) {
	choice := 0
	var note []rune
	var pending []byte
	buf := make([]byte, 32)
	for {
		n, err := in.Read(buf)
		if err != nil {
			return 0, "", err
		}
		for _, b := range buf[:n] {
			switch b {
			case '1', '2', '3':
				if len(note) == 0 {
					choice = int(b - '1')
					break
				}
				note = append(note, rune(b))
			case 'y', 'Y':
				if len(note) == 0 {
					return 0, "", nil
				}
				note = append(note, rune(b))
			case 'n', 'N':
				if len(note) == 0 {
					return 2, "", nil
				}
				note = append(note, rune(b))
			case 3, 0x1b:
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
				pending = append(pending, b)
				for len(pending) > 0 {
					r, size := utf8.DecodeRune(pending)
					if r == utf8.RuneError && !utf8.FullRune(pending) {
						break
					}
					pending = pending[size:]
					if r == utf8.RuneError {
						continue
					}
					note = append(note, r)
				}
			}
			if paint != nil {
				_ = paint(choice, string(note))
			}
		}
	}
}

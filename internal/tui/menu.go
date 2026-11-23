package tui

import "io"

// DecideMenu reads a footer selection. Arrow keys move. Enter confirms.
// Esc cancels with a negative choice.
func DecideMenu(in io.Reader, n int, paint func(int) error) (int, error) {
	if n <= 0 {
		return -1, nil
	}
	choice := 0
	sawEsc := false
	buf := make([]byte, 32)
	for {
		got, err := in.Read(buf)
		if err != nil {
			return -1, err
		}
		chunk := buf[:got]
		if sawEsc {
			sawEsc = false
			if len(chunk) == 0 || (chunk[0] != '[' && chunk[0] != 'O') {
				return -1, nil
			}
			chunk = append([]byte{0x1b}, chunk...)
		}
		for i := 0; i < len(chunk); i++ {
			b := chunk[i]
			if b == 0x1b {
				if i+1 >= len(chunk) {
					sawEsc = true
					break
				}
				if chunk[i+1] == '[' || chunk[i+1] == 'O' {
					if i+2 >= len(chunk) {
						break
					}
					switch chunk[i+2] {
					case 'A':
						if choice > 0 {
							choice--
						}
					case 'B':
						if choice+1 < n {
							choice++
						}
					}
					i += 2
					if paint != nil {
						_ = paint(choice)
					}
					continue
				}
				return -1, nil
			}
			switch b {
			case '1':
				choice = 0
			case '2':
				if n > 1 {
					choice = 1
				}
			case '3':
				if n > 2 {
					choice = 2
				}
			case '\r', '\n':
				return choice, nil
			case 3:
				return -1, nil
			}
			if paint != nil && (b == '1' || b == '2' || b == '3') {
				_ = paint(choice)
			}
		}
	}
}

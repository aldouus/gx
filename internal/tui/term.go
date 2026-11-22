package tui

import "os"

// IsTerminal reports whether fd is a terminal.
func IsTerminal(fd int) bool { return isTerminal(fd) }

// EnterRaw puts the terminal into raw mode and returns a restore function.
func EnterRaw(fd int) (func(), error) {
	old, err := makeRaw(fd)
	if err != nil {
		return nil, err
	}
	return func() { restore(fd, old) }, nil
}

// WindowSize returns rows and columns.
func WindowSize(fd int) (int, int) { return size(fd) }

// StdinFD is the process standard input.
func StdinFD() int { return int(os.Stdin.Fd()) }

// StdoutFD is the process standard output.
func StdoutFD() int { return int(os.Stdout.Fd()) }

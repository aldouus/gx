//go:build linux

package tui

import (
	"syscall"
	"unsafe"
)

func isTerminal(fd int) bool {
	var t syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.TCGETS, uintptr(unsafe.Pointer(&t)))
	return errno == 0
}

func makeRaw(fd int) (syscall.Termios, error) {
	var old syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.TCGETS, uintptr(unsafe.Pointer(&old)))
	if errno != 0 {
		return old, errno
	}
	next := old
	next.Lflag &^= syscall.ECHO | syscall.ICANON | syscall.ISIG
	next.Cc[syscall.VMIN] = 1
	next.Cc[syscall.VTIME] = 0
	_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.TCSETS, uintptr(unsafe.Pointer(&next)))
	if errno != 0 {
		return old, errno
	}
	return old, nil
}

func restore(fd int, old syscall.Termios) {
	syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.TCSETS, uintptr(unsafe.Pointer(&old)))
}

type winsize struct {
	Row, Col, Xpixel, Ypixel uint16
}

func size(fd int) (rows, cols int) {
	var ws winsize
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&ws)))
	if errno != 0 || ws.Col == 0 {
		return 24, 80
	}
	return int(ws.Row), int(ws.Col)
}

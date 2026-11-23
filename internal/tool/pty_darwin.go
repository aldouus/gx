//go:build darwin

package tool

import (
	"os"
	"syscall"
	"unsafe"
)

const (
	darwinPtyGrant = 0x20007454
	darwinPtyUnlk  = 0x20007452
	darwinPtyName  = 0x40807453
)

func openPTY() (*os.File, *os.File, error) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, err
	}
	if err := ioctl(master.Fd(), darwinPtyGrant, 0); err != nil {
		master.Close()
		return nil, nil, err
	}
	if err := ioctl(master.Fd(), darwinPtyUnlk, 0); err != nil {
		master.Close()
		return nil, nil, err
	}
	var name [128]byte
	if err := ioctl(master.Fd(), darwinPtyName, uintptr(unsafe.Pointer(&name[0]))); err != nil {
		master.Close()
		return nil, nil, err
	}
	n := 0
	for n < len(name) && name[n] != 0 {
		n++
	}
	slave, err := os.OpenFile(string(name[:n]), os.O_RDWR, 0)
	if err != nil {
		master.Close()
		return nil, nil, err
	}
	return master, slave, nil
}

func ioctl(fd uintptr, req uint, arg uintptr) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(req), arg)
	if errno != 0 {
		return errno
	}
	return nil
}

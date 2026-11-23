//go:build !darwin && !linux

package tool

import (
	"errors"
	"os"
)

func openPTY() (*os.File, *os.File, error) {
	return nil, nil, errors.New("pty is not available on this platform")
}

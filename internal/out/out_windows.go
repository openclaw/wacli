//go:build windows

package out

import (
	"errors"
	"syscall"

	"golang.org/x/sys/windows"
)

func isPlatformBrokenPipe(err error) bool {
	return errors.Is(err, syscall.EPIPE) || errors.Is(err, windows.ERROR_BROKEN_PIPE) || errors.Is(err, windows.ERROR_NO_DATA)
}

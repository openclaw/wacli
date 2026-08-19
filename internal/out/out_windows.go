//go:build windows

package out

import (
	"errors"
	"syscall"

	"golang.org/x/sys/windows"
)

func isPlatformBrokenPipe(err error) bool {
	if errors.Is(err, syscall.EPIPE) {
		return true
	}
	if errors.Is(err, windows.ERROR_BROKEN_PIPE) || errors.Is(err, windows.ERROR_NO_DATA) {
		return true
	}
	return isWindowsClosedPipeErrno(err)
}

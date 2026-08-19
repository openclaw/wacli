package out

import (
	"errors"
	"syscall"
)

// Windows closed-pipe results from Go's os.Pipe and process helpers.
const (
	windowsErrorBrokenPipe syscall.Errno = 109 // ERROR_BROKEN_PIPE
	windowsErrorNoData     syscall.Errno = 232 // ERROR_NO_DATA
)

func isWindowsClosedPipeErrno(err error) bool {
	var errno syscall.Errno
	return errors.As(err, &errno) && (errno == windowsErrorBrokenPipe || errno == windowsErrorNoData)
}

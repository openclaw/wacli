//go:build !windows

package out

import (
	"errors"
	"syscall"
)

func isPlatformBrokenPipe(err error) bool {
	return errors.Is(err, syscall.EPIPE)
}

//go:build !windows

package lock

import (
	"syscall"
)

func flock(fd int, how int) error {
	return syscall.Flock(fd, how)
}

const (
	lockEx = syscall.LOCK_EX
	lockNb = syscall.LOCK_NB
	lockUn = syscall.LOCK_UN
)

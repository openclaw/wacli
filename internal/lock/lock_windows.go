//go:build windows

package lock

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32         = windows.NewLazyDLL("kernel32.dll")
	procLockFileEx   = kernel32.NewProc("LockFileEx")
	procUnlockFileEx = kernel32.NewProc("UnlockFileEx")
)

const (
	lockfileFailImmediately = 0x00000001
	lockfileExclusiveLock   = 0x00000002
)

func flock(fd int, how int) error {
	handle := windows.Handle(fd)

	if how == lockUn {
		return unlockFileEx(handle)
	}

	var flags uint32
	if how&lockEx != 0 {
		flags |= lockfileExclusiveLock
	}
	if how&lockNb != 0 {
		flags |= lockfileFailImmediately
	}

	return lockFileEx(handle, flags)
}

func lockFileEx(handle windows.Handle, flags uint32) error {
	var overlapped windows.Overlapped
	ret, _, err := procLockFileEx.Call(
		uintptr(handle),
		uintptr(flags),
		uintptr(0), // dwReserved
		uintptr(1), // nNumberOfBytesToLockLow
		uintptr(0), // nNumberOfBytesToLockHigh
		uintptr(unsafe.Pointer(&overlapped)),
	)
	if ret == 0 {
		return err
	}
	return nil
}

func unlockFileEx(handle windows.Handle) error {
	var overlapped windows.Overlapped
	ret, _, err := procUnlockFileEx.Call(
		uintptr(handle),
		uintptr(0), // dwReserved
		uintptr(1), // nNumberOfBytesToUnlockLow
		uintptr(0), // nNumberOfBytesToUnlockHigh
		uintptr(unsafe.Pointer(&overlapped)),
	)
	if ret == 0 {
		return err
	}
	return nil
}

const (
	lockEx = 2
	lockNb = 1
	lockUn = 0
)

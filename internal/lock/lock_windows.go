//go:build windows

package lock

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	lockfileExclusiveLock = 0x02
	lockfileFailImmediately = 0x01
)

var (
	modkernel32      = windows.NewLazySystemDLL("kernel32.dll")
	procLockFileEx   = modkernel32.NewProc("LockFileEx")
	procUnlockFileEx = modkernel32.NewProc("UnlockFileEx")
)

func lockFile(f *os.File) error {
	// Lock the first byte (exclusive, non-blocking).
	ol := new(windows.Overlapped)
	r1, _, err := procLockFileEx.Call(
		uintptr(f.Fd()),
		uintptr(lockfileExclusiveLock|lockfileFailImmediately),
		0,
		1, 0,
		uintptr(unsafe.Pointer(ol)),
	)
	if r1 == 0 {
		return err
	}
	return nil
}

func unlockFile(f *os.File) {
	ol := new(windows.Overlapped)
	procUnlockFileEx.Call(
		uintptr(f.Fd()),
		0,
		1, 0,
		uintptr(unsafe.Pointer(ol)),
	)
}

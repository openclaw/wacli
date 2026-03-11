package lock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// ErrLocked is a sentinel error indicating that the store is locked by another process.
// Use errors.Is(err, ErrLocked) to distinguish lock contention from other lock failures.
var ErrLocked = errors.New("store is locked")

// ContentionError is returned when lock acquisition fails because another process
// holds the flock. It wraps ErrLocked and the underlying syscall error.
type ContentionError struct {
	cause error
	info  string
}

func (e *ContentionError) Error() string {
	if e.info != "" {
		return fmt.Sprintf("store is locked (another wacli is running?): %v (%s)", e.cause, e.info)
	}
	return fmt.Sprintf("store is locked (another wacli is running?): %v", e.cause)
}

func (e *ContentionError) Unwrap() error {
	return e.cause
}

func (e *ContentionError) Is(target error) bool {
	return target == ErrLocked
}

// isLockContention returns true if the error indicates another process holds the flock.
func isLockContention(err error) bool {
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
}

type Lock struct {
	path string
	f    *os.File
}

func Acquire(storeDir string) (*Lock, error) {
	if err := os.MkdirAll(storeDir, 0700); err != nil {
		return nil, fmt.Errorf("create store dir: %w", err)
	}
	path := filepath.Join(storeDir, "LOCK")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if isLockContention(err) {
			b, _ := os.ReadFile(path)
			info := strings.TrimSpace(string(b))
			return nil, &ContentionError{cause: err, info: info}
		}
		return nil, fmt.Errorf("flock lock file: %w", err)
	}

	_ = f.Truncate(0)
	_, _ = f.Seek(0, 0)
	_, _ = fmt.Fprintf(f, "pid=%d\nacquired_at=%s\n", os.Getpid(), time.Now().Format(time.RFC3339Nano))
	_ = f.Sync()

	return &Lock{path: path, f: f}, nil
}

func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	err := l.f.Close()
	l.f = nil
	return err
}

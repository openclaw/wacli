package lock

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type Lock struct {
	path string
	f    *os.File
}

func Acquire(storeDir string) (*Lock, error) {
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		return nil, fmt.Errorf("create store dir: %w", err)
	}
	path := filepath.Join(storeDir, "LOCK")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_, _ = f.Seek(0, 0)
		b, _ := os.ReadFile(path)
		_ = f.Close()
		info := strings.TrimSpace(string(b))
		if info != "" {
			return nil, fmt.Errorf("store is locked (another wacli is running?): %w (%s)", err, info)
		}
		return nil, fmt.Errorf("store is locked (another wacli is running?): %w", err)
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

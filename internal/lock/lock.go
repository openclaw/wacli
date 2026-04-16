package lock

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

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

	if err := flock(int(f.Fd()), lockEx|lockNb); err != nil {
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
	_ = flock(int(l.f.Fd()), lockUn)
	err := l.f.Close()
	l.f = nil
	return err
}

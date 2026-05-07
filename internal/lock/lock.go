package lock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/openclaw/wacli/internal/fsutil"
)

type Lock struct {
	path string
	f    *os.File
}

var ErrLocked = errors.New("store locked")

func Acquire(storeDir string) (*Lock, error) {
	if err := fsutil.EnsurePrivateDir(storeDir); err != nil {
		return nil, fmt.Errorf("create store dir: %w", err)
	}
	path := filepath.Join(storeDir, "LOCK")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}

	if err := lockFile(f); err != nil {
		_, _ = f.Seek(0, 0)
		b, _ := os.ReadFile(path)
		_ = f.Close()
		info := strings.TrimSpace(string(b))
		if info != "" {
			return nil, fmt.Errorf("store is locked (another wacli is running?): %w: %w (%s)", ErrLocked, err, info)
		}
		return nil, fmt.Errorf("store is locked (another wacli is running?): %w: %w", ErrLocked, err)
	}

	_ = f.Truncate(0)
	_, _ = f.Seek(0, 0)
	_, _ = fmt.Fprintf(f, "pid=%d\nacquired_at=%s\n", os.Getpid(), time.Now().Format(time.RFC3339Nano))
	_ = f.Sync()

	return &Lock{path: path, f: f}, nil
}

func AcquireWithTimeout(ctx context.Context, storeDir string, wait time.Duration) (*Lock, error) {
	if wait <= 0 {
		return Acquire(storeDir)
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	var lastErr error
	for {
		lk, err := Acquire(storeDir)
		if err == nil {
			return lk, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, fmt.Errorf("timed out waiting for store lock after %s: %w", wait, lastErr)
		case <-ticker.C:
		}
	}
}

func IsLocked(err error) bool {
	return errors.Is(err, ErrLocked)
}

func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	_ = unlockFile(l.f)
	err := l.f.Close()
	l.f = nil
	return err
}

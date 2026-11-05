//go:build !windows

// Package delegate provides the local, same-user transport for sync delegation.
package delegate

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
)

func socketPath(storeDir string) string {
	return filepath.Join(storeDir, ".send.sock")
}

func Dial(ctx context.Context, storeDir string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", socketPath(storeDir))
}

func Listen(storeDir string) (net.Listener, error) {
	path := socketPath(storeDir)
	info, err := os.Lstat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("%s exists and is not a socket", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, err
	}
	return ln, nil
}

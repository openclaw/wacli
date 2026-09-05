package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/openclaw/wacli/internal/fsutil"
)

const sessionRevokedFilename = "SESSION_REVOKED"

// MarkSessionRevoked records a terminal remote logout independently of
// whatsmeow's session-row cleanup. This prevents a stale device row from being
// reported as authenticated if shutdown races the dependency's asynchronous
// delete.
func MarkSessionRevoked(storeDir, reason string) error {
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		return fmt.Errorf("create store directory: %w", err)
	}
	contents := fmt.Sprintf("reason=%s\nrecorded_at=%s\n", reason, nowUTC().Format(time.RFC3339))
	if err := fsutil.WritePrivateFileAtomic(filepath.Join(storeDir, sessionRevokedFilename), []byte(contents)); err != nil {
		return fmt.Errorf("write session revoked marker: %w", err)
	}
	return nil
}

// SessionRevoked reports whether the last observed terminal session state was
// a remote logout.
func SessionRevoked(storeDir string) (bool, error) {
	_, err := os.Stat(filepath.Join(storeDir, sessionRevokedFilename))
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	default:
		return false, fmt.Errorf("read session revoked marker: %w", err)
	}
}

// clearSessionRevoked removes stale logout state only after WhatsApp confirms a
// new live connection.
func ClearSessionRevoked(storeDir string) error {
	err := os.Remove(filepath.Join(storeDir, sessionRevokedFilename))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("clear session revoked marker: %w", err)
	}
	return nil
}

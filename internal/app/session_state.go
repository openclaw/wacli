package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/openclaw/wacli/internal/fsutil"
	"go.mau.fi/whatsmeow/types/events"
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

// ClearSessionRevoked removes stale logout state only after WhatsApp confirms a
// new live connection.
func ClearSessionRevoked(storeDir string) error {
	err := os.Remove(filepath.Join(storeDir, sessionRevokedFilename))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("clear session revoked marker: %w", err)
	}
	return nil
}

// A terminal rejection cannot be undone by a late Connected callback from the
// same attempt. A new command or sync run gets a new observation.
type sessionObservation struct {
	mu             sync.Mutex
	storeDir       string
	loggedIn       func() bool
	confirmed      bool
	terminalErr    error
	persistenceErr error
	changed        chan struct{}
}

func newSessionObservation(storeDir string, loggedIn func() bool) *sessionObservation {
	return &sessionObservation{storeDir: storeDir, loggedIn: loggedIn, changed: make(chan struct{}, 1)}
}

func (s *sessionObservation) notify() {
	select {
	case s.changed <- struct{}{}:
	default:
	}
}

func (s *sessionObservation) confirmLoginLocked() error {
	if s.terminalErr != nil || s.confirmed || !s.loggedIn() {
		return nil
	}
	s.persistenceErr = ClearSessionRevoked(s.storeDir)
	s.confirmed = s.persistenceErr == nil
	return s.persistenceErr
}

func (s *sessionObservation) confirmLogin() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.notify()
	return s.confirmLoginLocked()
}

func (s *sessionObservation) observe(evt any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.notify()
	switch v := evt.(type) {
	case *events.Connected:
		return s.confirmLoginLocked()
	case *events.LoggedOut:
		s.confirmed = false
		s.terminalErr = fmt.Errorf("WhatsApp session was revoked: %s", v.Reason)
		s.persistenceErr = MarkSessionRevoked(s.storeDir, v.Reason.String())
	case *events.ConnectFailure:
		s.confirmed = false
		s.terminalErr = fmt.Errorf("WhatsApp login failed: %s", v.Reason)
		if v.Reason.IsLoggedOut() {
			s.persistenceErr = MarkSessionRevoked(s.storeDir, v.Reason.String())
		}
	case *events.Disconnected:
		s.confirmed = false
		if s.terminalErr == nil {
			s.terminalErr = fmt.Errorf("disconnected before WhatsApp login completed")
		}
	}
	return s.persistenceErr
}

func (s *sessionObservation) waitForLogin(ctx context.Context) error {
	for {
		s.mu.Lock()
		confirmed, err := s.confirmed, s.terminalErr
		if err == nil {
			err = s.persistenceErr
		}
		s.mu.Unlock()
		if err != nil {
			return err
		}
		if confirmed {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.changed:
		}
	}
}

func (a *App) observeSessionState(state *sessionObservation, evt any) {
	if err := state.observe(evt); err != nil {
		a.emitWarning("session_state_persistence_failed",
			fmt.Sprintf("warning: failed to persist session state: %v", err),
			map[string]any{"error": err.Error()})
	}
}

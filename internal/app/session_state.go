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

// One observer owns marker writes for the client lifetime. Revocation remains
// terminal for that client, including delayed Connected callbacks.
type sessionObservation struct {
	mu             sync.Mutex
	storeDir       string
	revoked        bool
	confirmed      bool
	terminalErr    error
	persistenceErr error
	changed        chan struct{}
}

func newSessionObservation(storeDir string) *sessionObservation {
	return &sessionObservation{storeDir: storeDir, changed: make(chan struct{}, 1)}
}

func (s *sessionObservation) notify() {
	select {
	case s.changed <- struct{}{}:
	default:
	}
}

func (s *sessionObservation) prepareConnect(isConnected func() bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revoked {
		return s.terminalErr
	}
	if !isConnected() {
		s.confirmed = false
		s.terminalErr = nil
		s.persistenceErr = nil
	}
	return nil
}

func (s *sessionObservation) confirmLogin() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.notify()
	if s.revoked || s.confirmed {
		return nil
	}
	s.terminalErr = nil
	s.persistenceErr = ClearSessionRevoked(s.storeDir)
	s.confirmed = s.persistenceErr == nil
	return s.persistenceErr
}

func (s *sessionObservation) observe(evt any) error {
	switch v := evt.(type) {
	case *events.Connected:
		return s.confirmLogin()
	case *events.LoggedOut:
		return s.rejectLogin(fmt.Errorf("WhatsApp session was revoked: %s", v.Reason), v.Reason.String())
	case *events.ConnectFailure:
		var revokedReason string
		if v.Reason.IsLoggedOut() {
			revokedReason = v.Reason.String()
		}
		return s.rejectLogin(fmt.Errorf("WhatsApp login failed: %s", v.Reason), revokedReason)
	case *events.ClientOutdated:
		return s.rejectLogin(fmt.Errorf("WhatsApp client is outdated; update wacli and try again"), "")
	case *events.TemporaryBan:
		return s.rejectLogin(fmt.Errorf("WhatsApp account is temporarily banned: %s", v), "")
	case *events.StreamReplaced:
		return s.rejectLogin(fmt.Errorf("WhatsApp stream was replaced by another client"), "")
	case *events.CATRefreshError:
		return s.rejectLogin(fmt.Errorf("failed to refresh WhatsApp authentication token: %w", v.Error), "")
	case *events.StreamError:
		return s.rejectLogin(fmt.Errorf("WhatsApp stream error before login: %s", v.Code), "")
	case *events.ManualLoginReconnect:
		return s.rejectLogin(fmt.Errorf("WhatsApp login requires a reconnect"), "")
	case *events.Disconnected:
		s.mu.Lock()
		s.confirmed = false
		s.mu.Unlock()
		s.notify()
		return nil
	default:
		return nil
	}
}

func (s *sessionObservation) rejectLogin(err error, revokedReason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.notify()
	s.confirmed = false
	if s.terminalErr == nil || revokedReason != "" {
		s.terminalErr = err
	}
	if revokedReason != "" {
		s.revoked = true
		s.persistenceErr = MarkSessionRevoked(s.storeDir, revokedReason)
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

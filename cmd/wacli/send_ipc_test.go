package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openclaw/wacli/internal/lock"
)

func TestTryDelegateSendFallsBackWhenSocketUnavailable(t *testing.T) {
	dir := t.TempDir()
	flags := &rootFlags{storeDir: dir}
	lockErr := fmt.Errorf("held: %w", lock.ErrLocked)

	_, delegated, err := tryDelegateSend(context.Background(), flags, lockErr, sendDelegateRequest{Kind: "text"})
	if delegated {
		t.Fatalf("delegated = true, want false for missing socket")
	}
	if !errors.Is(err, lock.ErrLocked) {
		t.Fatalf("error = %v, want original lock error", err)
	}
}

func TestTryDelegateSendDoesNotDelegateNonLockErrors(t *testing.T) {
	orig := errors.New("open store")

	_, delegated, err := tryDelegateSend(context.Background(), &rootFlags{}, orig, sendDelegateRequest{Kind: "text"})
	if delegated {
		t.Fatalf("delegated = true, want false")
	}
	if !errors.Is(err, orig) {
		t.Fatalf("error = %v, want original", err)
	}
}

func TestExecuteDelegatedSendRejectsBadVersionBeforeAppUse(t *testing.T) {
	_, err := executeDelegatedSend(context.Background(), nil, sendDelegateRequest{
		Version: sendDelegateVersion + 1,
		Kind:    "text",
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported send delegate version") {
		t.Fatalf("error = %v", err)
	}
}

func TestRemoveStaleSendDelegateSocketRefusesRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), sendDelegateSocketName)
	if err := os.WriteFile(path, []byte("not a socket"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := removeStaleSendDelegateSocket(path); err == nil || !strings.Contains(err.Error(), "not a socket") {
		t.Fatalf("error = %v, want not a socket", err)
	}
}

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

func TestRunSendOperationRetriesRetryableError(t *testing.T) {
	var reconnects int
	attempts := 0

	got, err := runSendOperation(context.Background(), func(ctx context.Context) error {
		reconnects++
		return nil
	}, func(ctx context.Context) (string, error) {
		attempts++
		if attempts == 1 {
			return "", fmt.Errorf("failed to get device list: failed to send usync query: %w", whatsmeow.ErrIQTimedOut)
		}
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("runSendOperation: %v", err)
	}
	if got != "ok" {
		t.Fatalf("expected ok, got %q", got)
	}
	if reconnects != 1 {
		t.Fatalf("expected 1 reconnect, got %d", reconnects)
	}
}

func TestRunSendOperationDoesNotRetryValidationError(t *testing.T) {
	var reconnects int

	_, err := runSendOperation(context.Background(), func(ctx context.Context) error {
		reconnects++
		return nil
	}, func(ctx context.Context) (string, error) {
		return "", errors.New("permission denied")
	})
	if err == nil {
		t.Fatalf("expected error")
	}
	if reconnects != 0 {
		t.Fatalf("expected no reconnect, got %d", reconnects)
	}
}

func TestRunSendAttemptTimesOut(t *testing.T) {
	_, err := runSendAttempt(context.Background(), 20*time.Millisecond, func(ctx context.Context) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	})
	if err == nil {
		t.Fatalf("expected timeout error")
	}
	if err.Error() != "send timed out after 20ms" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWaitForPostSendRetryReceipts(t *testing.T) {
	start := time.Now()
	waitForPostSendRetryReceipts(context.Background(), 10*time.Millisecond)
	if elapsed := time.Since(start); elapsed < 10*time.Millisecond {
		t.Fatalf("wait elapsed %s, want at least 10ms", elapsed)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start = time.Now()
	waitForPostSendRetryReceipts(ctx, time.Minute)
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("canceled wait elapsed %s, want quick return", elapsed)
	}

	start = time.Now()
	waitForPostSendRetryReceipts(context.Background(), 0)
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("disabled wait elapsed %s, want quick return", elapsed)
	}
}

func TestIsRetryableSendError(t *testing.T) {
	if !isRetryableSendError(fmt.Errorf("wrapped: %w", whatsmeow.ErrIQTimedOut)) {
		t.Fatalf("expected ErrIQTimedOut to be retryable")
	}
	if !isRetryableSendError(errors.New("failed to get user info for 123@s.whatsapp.net to fill LID cache: failed to send usync query: info query timed out")) {
		t.Fatalf("expected wrapped usync timeout to be retryable")
	}
	if isRetryableSendError(errors.New("permission denied")) {
		t.Fatalf("did not expect arbitrary error to be retryable")
	}
}

func TestWarnRapidSendIfNeededWarnsAndUpdatesMarker(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	var stderr bytes.Buffer

	if err := warnRapidSendIfNeeded(dir, now, &stderr); err != nil {
		t.Fatalf("first warning check: %v", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("first send warned: %q", stderr.String())
	}

	if err := warnRapidSendIfNeeded(dir, now.Add(time.Second), &stderr); err != nil {
		t.Fatalf("second warning check: %v", err)
	}
	if got := stderr.String(); !strings.Contains(got, "warning: send command was invoked 1s after the previous send") {
		t.Fatalf("expected rapid-send warning, got %q", got)
	}

	info, err := os.Stat(filepath.Join(dir, lastSendAttemptFile))
	if err != nil {
		t.Fatalf("stat marker: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("marker mode = %04o, want 0600", got)
	}
}

func TestWarnRapidSendIfNeededSkipsOldOrInvalidMarker(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(dir, lastSendAttemptFile)

	if err := os.WriteFile(path, []byte(now.Add(-rapidSendWarningThreshold).Format(time.RFC3339Nano)), 0o600); err != nil {
		t.Fatalf("write old marker: %v", err)
	}
	var stderr bytes.Buffer
	if err := warnRapidSendIfNeeded(dir, now, &stderr); err != nil {
		t.Fatalf("old marker warning check: %v", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("old marker warned: %q", stderr.String())
	}

	if err := os.WriteFile(path, []byte("not a timestamp"), 0o600); err != nil {
		t.Fatalf("write invalid marker: %v", err)
	}
	if err := warnRapidSendIfNeeded(dir, now.Add(time.Second), &stderr); err != nil {
		t.Fatalf("invalid marker warning check: %v", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("invalid marker warned: %q", stderr.String())
	}
}

type mockUserInfoClient struct {
	getUserInfo  func(ctx context.Context, jids []types.JID) (map[types.JID]types.UserInfo, error)
	isOnWhatsApp func(ctx context.Context, phones []string) ([]types.IsOnWhatsAppResponse, error)
}

func (m *mockUserInfoClient) GetUserInfo(ctx context.Context, jids []types.JID) (map[types.JID]types.UserInfo, error) {
	if m.getUserInfo == nil {
		return nil, nil
	}
	return m.getUserInfo(ctx, jids)
}

func (m *mockUserInfoClient) IsOnWhatsApp(ctx context.Context, phones []string) ([]types.IsOnWhatsAppResponse, error) {
	if m.isOnWhatsApp == nil {
		return nil, nil
	}
	return m.isOnWhatsApp(ctx, phones)
}

func TestWarmupRecipientSkipNonUserServer(t *testing.T) {
	called := false
	mock := &mockUserInfoClient{
		getUserInfo: func(ctx context.Context, jids []types.JID) (map[types.JID]types.UserInfo, error) {
			called = true
			return nil, nil
		},
	}

	var stderr bytes.Buffer
	groupJID := types.NewJID("12345", types.GroupServer)
	warmupRecipient(context.Background(), mock, groupJID, &stderr)
	if called {
		t.Fatal("GetUserInfo should not be called for group JIDs")
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}
}

func TestWarmupRecipientCallsGetUserInfoForUserServer(t *testing.T) {
	called := false
	mock := &mockUserInfoClient{
		getUserInfo: func(ctx context.Context, jids []types.JID) (map[types.JID]types.UserInfo, error) {
			called = true
			if len(jids) != 1 {
				t.Fatalf("expected 1 JID, got %d", len(jids))
			}
			if jids[0].String() != "15551234567@s.whatsapp.net" {
				t.Fatalf("unexpected JID: %s", jids[0].String())
			}
			return nil, nil
		},
	}

	var stderr bytes.Buffer
	userJID := types.NewJID("15551234567", types.DefaultUserServer)
	warmupRecipient(context.Background(), mock, userJID, &stderr)
	if !called {
		t.Fatal("GetUserInfo should be called for user JIDs")
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}
}

func TestWarmupRecipientCanonicalizesRegisteredPhone(t *testing.T) {
	canonical := types.NewJID("15551234567", types.DefaultUserServer)
	mock := &mockUserInfoClient{
		isOnWhatsApp: func(ctx context.Context, phones []string) ([]types.IsOnWhatsAppResponse, error) {
			if len(phones) != 1 || phones[0] != "+15559991234567" {
				t.Fatalf("unexpected phone query: %v", phones)
			}
			return []types.IsOnWhatsAppResponse{{
				JID:  canonical,
				IsIn: true,
			}}, nil
		},
		getUserInfo: func(ctx context.Context, jids []types.JID) (map[types.JID]types.UserInfo, error) {
			if len(jids) != 1 || jids[0] != canonical {
				t.Fatalf("expected canonical JID %s, got %v", canonical, jids)
			}
			return nil, nil
		},
	}

	var stderr bytes.Buffer
	input := types.NewJID("15559991234567", types.DefaultUserServer)
	got := warmupRecipient(context.Background(), mock, input, &stderr)
	if got != canonical {
		t.Fatalf("warmupRecipient returned %s, want %s", got, canonical)
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}
}

func TestWarmupRecipientLogsErrorToStderr(t *testing.T) {
	mock := &mockUserInfoClient{
		getUserInfo: func(ctx context.Context, jids []types.JID) (map[types.JID]types.UserInfo, error) {
			return nil, errors.New("simulated failure")
		},
	}

	var stderr bytes.Buffer
	userJID := types.NewJID("15551234567", types.DefaultUserServer)
	warmupRecipient(context.Background(), mock, userJID, &stderr)
	if stderr.Len() == 0 {
		t.Fatal("expected stderr output on error")
	}
	if !strings.Contains(stderr.String(), "warn: send warmup for") {
		t.Fatalf("expected warning in stderr, got: %q", stderr.String())
	}
}

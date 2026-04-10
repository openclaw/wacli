package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
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

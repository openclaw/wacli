package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openclaw/wacli/internal/fsutil"
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

func TestSendDelegateRequestPreservesEphemeralInJSON(t *testing.T) {
	raw, err := json.Marshal(sendDelegateRequest{
		Version:              sendDelegateVersion,
		Kind:                 "text",
		Message:              "hello",
		Ephemeral:            true,
		EphemeralDuration:    "7d",
		EphemeralDurationSet: true,
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"ephemeral":true`) {
		t.Fatalf("encoded request missing ephemeral flag: %s", raw)
	}
	if !strings.Contains(string(raw), `"ephemeral_duration":"7d"`) {
		t.Fatalf("encoded request missing ephemeral duration: %s", raw)
	}
	if !strings.Contains(string(raw), `"ephemeral_duration_set":true`) {
		t.Fatalf("encoded request missing ephemeral duration set flag: %s", raw)
	}

	var got sendDelegateRequest
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !got.Ephemeral {
		t.Fatalf("Ephemeral = false, want true")
	}
	if got.EphemeralDuration != "7d" {
		t.Fatalf("EphemeralDuration = %q, want 7d", got.EphemeralDuration)
	}
	if !got.EphemeralDurationSet {
		t.Fatalf("EphemeralDurationSet = false, want true")
	}
}

func TestSendDelegateRequestPreservesPresenceInJSON(t *testing.T) {
	raw, err := json.Marshal(sendDelegateRequest{
		Version:       sendDelegateVersion,
		Kind:          "presence",
		To:            "+33600000000",
		PresenceState: "composing",
		PresenceMedia: "audio",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"presence_state":"composing"`) {
		t.Fatalf("encoded request missing presence state: %s", raw)
	}
	if !strings.Contains(string(raw), `"presence_media":"audio"`) {
		t.Fatalf("encoded request missing presence media: %s", raw)
	}

	var got sendDelegateRequest
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Kind != "presence" {
		t.Fatalf("Kind = %q, want presence", got.Kind)
	}
	if got.PresenceState != "composing" {
		t.Fatalf("PresenceState = %q, want composing", got.PresenceState)
	}
	if got.PresenceMedia != "audio" {
		t.Fatalf("PresenceMedia = %q, want audio", got.PresenceMedia)
	}
}

func TestRemoveStaleSendDelegateSocketRefusesRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), sendDelegateSocketName)
	if err := fsutil.WritePrivateFile(path, []byte("not a socket")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := removeStaleSendDelegateSocket(path); err == nil || !strings.Contains(err.Error(), "not a socket") {
		t.Fatalf("error = %v, want not a socket", err)
	}
}

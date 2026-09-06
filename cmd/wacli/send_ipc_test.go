package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/fsutil"
	"github.com/openclaw/wacli/internal/lock"
	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow/types"
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

func TestSendDelegateRequestPreservesReplyInJSON(t *testing.T) {
	raw, err := json.Marshal(sendDelegateRequest{
		Version:       sendDelegateVersion,
		Kind:          "text",
		To:            "15551234567@s.whatsapp.net",
		Message:       "reply",
		ReplyTo:       "quoted-message-id",
		ReplyToSender: "15557654321@s.whatsapp.net",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got sendDelegateRequest
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.ReplyTo != "quoted-message-id" {
		t.Fatalf("ReplyTo = %q", got.ReplyTo)
	}
	if got.ReplyToSender != "15557654321@s.whatsapp.net" {
		t.Fatalf("ReplyToSender = %q", got.ReplyToSender)
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

func TestMessagesEditDelegatesThroughSendSocketWhenStoreLocked(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	storeDir := shortPresenceDelegateStoreDir(t)
	lk, err := lock.Acquire(storeDir)
	if err != nil {
		t.Fatalf("lock store: %v", err)
	}
	defer lk.Release()

	server := startPresenceDelegateTestSocket(t, storeDir, func(req sendDelegateRequest) sendDelegateResponse {
		return sendDelegateResponse{
			OK: true, Sent: true, To: "123@s.whatsapp.net", ID: "sent-id", Target: req.ID,
		}
	})
	defer server.stop()

	stdout, stderr, err := runPresenceDelegateHelper(t, []string{
		"--store", storeDir, "--json", "--timeout", "750ms",
		"messages", "edit", "--chat", "123@s.whatsapp.net", "--id", "ABC",
		"--message", "edited", "--post-send-wait", "25ms",
	})
	if err != nil {
		t.Fatalf("messages edit failed: %v stdout=%q stderr=%q", err, stdout, stderr)
	}

	req := server.nextRequest(t)
	if req.Version != sendDelegateVersion || req.Kind != "edit" {
		t.Fatalf("delegate version/kind = %d/%q", req.Version, req.Kind)
	}
	if req.To != "123@s.whatsapp.net" || req.ID != "ABC" || req.Message != "edited" {
		t.Fatalf("delegate mutation target = %+v", req)
	}
	if req.TimeoutMS != 750 || req.PostSendWaitMS != 25 {
		t.Fatalf("delegate timeouts = command %dms post-send %dms", req.TimeoutMS, req.PostSendWaitMS)
	}
	if req.DeadlineUnixMS == 0 {
		t.Fatal("delegated request missing absolute command deadline")
	}
	if strings.Contains(stderr, "store is locked") {
		t.Fatalf("delegated command tried the direct store path: stderr=%q", stderr)
	}
	for _, want := range []string{`"edited":true`, `"id":"sent-id"`, `"target":"ABC"`} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout %q missing %s", stdout, want)
		}
	}
}

func TestSendFileDelegatesMediaAsThroughSendSocketWhenStoreLocked(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	storeDir := shortPresenceDelegateStoreDir(t)
	lk, err := lock.Acquire(storeDir)
	if err != nil {
		t.Fatalf("lock store: %v", err)
	}
	defer lk.Release()

	server := startPresenceDelegateTestSocket(t, storeDir, func(req sendDelegateRequest) sendDelegateResponse {
		return sendDelegateResponse{
			OK: true, Sent: true, To: "123@s.whatsapp.net", ID: "sent-id", File: map[string]string{"name": "song.mp3"},
		}
	})
	defer server.stop()

	stdout, stderr, err := runPresenceDelegateHelper(t, []string{
		"--store", storeDir, "--json", "--timeout", "750ms",
		"send", "file", "--to", "123@s.whatsapp.net", "--file", "song.mp3",
		"--mime", "audio/mpeg", "--as", "document", "--post-send-wait", "25ms",
	})
	if err != nil {
		t.Fatalf("send file failed: %v stdout=%q stderr=%q", err, stdout, stderr)
	}

	req := server.nextRequest(t)
	if req.Version != sendDelegateVersion || req.Kind != "file" {
		t.Fatalf("delegate version/kind = %d/%q", req.Version, req.Kind)
	}
	if req.To != "123@s.whatsapp.net" || req.MIME != "audio/mpeg" || req.As != "document" {
		t.Fatalf("delegate media options = %+v", req)
	}
	if req.TimeoutMS != 750 || req.PostSendWaitMS != 25 {
		t.Fatalf("delegate timeouts = command %dms post-send %dms", req.TimeoutMS, req.PostSendWaitMS)
	}
	if strings.Contains(stderr, "store is locked") || strings.Contains(stderr, "not authenticated") || strings.Contains(stderr, "not connected") {
		t.Fatalf("delegated command tried the direct store/client path: stderr=%q", stderr)
	}
	for _, want := range []string{`"sent":true`, `"id":"sent-id"`, `"name":"song.mp3"`} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout %q missing %s", stdout, want)
		}
	}
}

func TestExecuteDelegatedSendAcceptsEditKind(t *testing.T) {
	// Reaching app use proves the daemon dispatcher recognized the edit kind.
	defer func() { _ = recover() }()
	_, err := executeDelegatedSend(context.Background(), nil, sendDelegateRequest{
		Version: sendDelegateVersion,
		Kind:    "edit",
		To:      "123@s.whatsapp.net",
		ID:      "ABC",
		Message: "edited",
	})
	if err != nil && strings.Contains(err.Error(), "unsupported send kind") {
		t.Fatalf("edit rejected as unsupported kind: %v", err)
	}
}

func TestSendDelegateRequestPreservesMarkReadInJSON(t *testing.T) {
	read := true
	raw, err := json.Marshal(sendDelegateRequest{
		Version: sendDelegateVersion,
		Kind:    "mark_read",
		To:      "123@s.whatsapp.net",
		Read:    &read,
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"read":true`) {
		t.Fatalf("encoded request missing read flag: %s", raw)
	}

	var got sendDelegateRequest
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Kind != "mark_read" {
		t.Fatalf("Kind = %q, want mark_read", got.Kind)
	}
	if got.Read == nil || !*got.Read {
		t.Fatalf("Read = %v, want true", got.Read)
	}

	unread := false
	rawUnread, err := json.Marshal(sendDelegateRequest{
		Version: sendDelegateVersion,
		Kind:    "mark_read",
		To:      "123@s.whatsapp.net",
		Read:    &unread,
	})
	if err != nil {
		t.Fatalf("Marshal unread: %v", err)
	}
	var gotUnread sendDelegateRequest
	if err := json.Unmarshal(rawUnread, &gotUnread); err != nil {
		t.Fatalf("Unmarshal unread: %v", err)
	}
	if gotUnread.Read == nil || *gotUnread.Read {
		t.Fatalf("Read = %v, want false", gotUnread.Read)
	}
}

func TestExecuteDelegatedSendRoutesMarkRead(t *testing.T) {
	a, err := app.New(app.Options{StoreDir: t.TempDir()})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	t.Cleanup(a.Close)

	_, err = executeDelegatedSend(context.Background(), a, sendDelegateRequest{
		Version: sendDelegateVersion,
		Kind:    "mark_read",
	})
	if err == nil || !strings.Contains(err.Error(), "--to is required") {
		t.Fatalf("error = %v, want mark-read recipient validation", err)
	}
}

type delegatedMarkReadCall struct {
	chat types.JID
	read bool
}

type fakeDelegatedMarkReadApp struct {
	calls chan delegatedMarkReadCall
}

func (f *fakeDelegatedMarkReadApp) DB() *store.DB { return nil }

func (f *fakeDelegatedMarkReadApp) MarkChatRead(_ context.Context, chat types.JID, read bool) error {
	f.calls <- delegatedMarkReadCall{chat: chat, read: read}
	return nil
}

func TestChatsMarkReadDelegatesThroughProductionServerWhenStoreLocked(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	storeDir := shortPresenceDelegateStoreDir(t)
	lk, err := lock.Acquire(storeDir)
	if err != nil {
		t.Fatalf("lock store: %v", err)
	}
	defer lk.Release()

	fake := &fakeDelegatedMarkReadApp{calls: make(chan delegatedMarkReadCall, 2)}
	stop, err := startSendDelegateServerForStore(context.Background(), storeDir, sendSpacing{}, func(ctx context.Context, req sendDelegateRequest) (sendDelegateResponse, error) {
		if req.Version != sendDelegateVersion {
			return sendDelegateResponse{}, fmt.Errorf("unexpected delegated version %d", req.Version)
		}
		if req.Kind != "mark_read" {
			return sendDelegateResponse{}, fmt.Errorf("unexpected delegated kind %q", req.Kind)
		}
		return executeDelegatedMarkRead(ctx, fake, req)
	})
	if err != nil {
		t.Fatalf("start production delegate server: %v", err)
	}
	stopped := false
	defer func() {
		if !stopped {
			stop()
		}
	}()

	socketPath := sendDelegateSocketPath(storeDir)
	info, err := os.Lstat(socketPath)
	if err != nil {
		t.Fatalf("stat delegate socket: %v", err)
	}
	if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o600 {
		t.Fatalf("delegate socket mode = %v, want socket 0600", info.Mode())
	}

	tests := []struct {
		command string
		read    bool
	}{
		{command: "mark-read", read: true},
		{command: "mark-unread", read: false},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			stdout, stderr, err := runPresenceDelegateHelper(t, []string{
				"--store", storeDir, "--json", "--timeout", "750ms",
				"chats", tt.command, "--chat", "123@s.whatsapp.net",
			})
			if err != nil {
				t.Fatalf("chats %s failed: %v stdout=%q stderr=%q", tt.command, err, stdout, stderr)
			}

			select {
			case call := <-fake.calls:
				if call.chat.String() != "123@s.whatsapp.net" || call.read != tt.read {
					t.Fatalf("fake mark-read call = %+v, want chat 123@s.whatsapp.net read %t", call, tt.read)
				}
			case <-contextWithTestTimeout(t).Done():
				t.Fatal("timed out waiting for delegated mark-read call")
			}
			if strings.Contains(stderr, "store is locked") {
				t.Fatalf("delegated command returned lock error: stderr=%q", stderr)
			}
			for _, want := range []string{`"ok":true`, `"action":"` + tt.command + `"`, `"chat":"123@s.whatsapp.net"`} {
				if !strings.Contains(stdout, want) {
					t.Fatalf("stdout %q missing %s", stdout, want)
				}
			}
		})
	}

	stop()
	stopped = true
	if _, err := os.Lstat(socketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("delegate socket remains after stop: %v", err)
	}
}

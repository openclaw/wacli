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
	"time"

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

func TestSendDelegateRequestPreservesAllowSelfInJSON(t *testing.T) {
	raw, err := json.Marshal(sendDelegateRequest{
		Version:   sendDelegateVersion,
		Kind:      "text",
		AllowSelf: true,
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"allow_self":true`) {
		t.Fatalf("encoded request missing allow-self flag: %s", raw)
	}

	var got sendDelegateRequest
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !got.AllowSelf {
		t.Fatalf("AllowSelf = false, want true")
	}
}

func TestSendTextAllowSelfDelegatesThroughSendSocketWhenStoreLocked(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	storeDir := shortPresenceDelegateStoreDir(t)
	lk, err := lock.Acquire(storeDir)
	if err != nil {
		t.Fatalf("lock store: %v", err)
	}
	defer lk.Release()

	server := startPresenceDelegateTestSocket(t, storeDir, func(req sendDelegateRequest) sendDelegateResponse {
		return sendDelegateResponse{OK: true, Sent: true, To: "15551234567@s.whatsapp.net", ID: "self-id"}
	})
	defer server.stop()

	stdout, stderr, err := runPresenceDelegateHelper(t, []string{
		"--store", storeDir, "--json", "--timeout", "750ms",
		"send", "text", "--to", "+15551234567", "--message", "self-test", "--allow-self",
	})
	if err != nil {
		t.Fatalf("send text failed: %v stdout=%q stderr=%q", err, stdout, stderr)
	}

	req := server.nextRequest(t)
	if req.Version != sendDelegateVersion || req.Kind != "text" {
		t.Fatalf("delegate version/kind = %d/%q", req.Version, req.Kind)
	}
	if !req.AllowSelf {
		t.Fatalf("delegate AllowSelf = false, want true")
	}
	if req.To != "+15551234567" || req.Message != "self-test" {
		t.Fatalf("delegate text request = %+v", req)
	}
	if strings.Contains(stderr, "store is locked") {
		t.Fatalf("delegated command tried the direct store path: stderr=%q", stderr)
	}
	if !strings.Contains(stdout, `"sent":true`) || !strings.Contains(stdout, `"id":"self-id"`) {
		t.Fatalf("stdout %q missing delegated success", stdout)
	}
}

func TestSendTextAllowSelfPreservesOlderDelegateRejection(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	storeDir := shortPresenceDelegateStoreDir(t)
	lk, err := lock.Acquire(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	defer lk.Release()
	server := startPresenceDelegateTestSocket(t, storeDir, func(req sendDelegateRequest) sendDelegateResponse {
		// Older daemons ignore allow_self and retain their self-recipient guard.
		return sendDelegateResponse{OK: false, Error: errSelfTextRecipient.Error()}
	})
	defer server.stop()
	stdout, stderr, err := runPresenceDelegateHelper(t, []string{
		"--store", storeDir, "--json", "--timeout", "2s",
		"send", "text", "--to", "+15551234567", "--message", "self-test", "--allow-self",
	})
	if err == nil || !strings.Contains(stderr, errSelfTextRecipient.Error()) || strings.Contains(stdout, `"sent":true`) {
		t.Fatalf("older delegate rejection: err=%v stdout=%q stderr=%q", err, stdout, stderr)
	}
	if !server.nextRequest(t).AllowSelf {
		t.Fatal("opt-in did not reach the delegate")
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
	chat    types.JID
	read    bool
	receipt bool
}

type fakeDelegatedMarkReadApp struct {
	calls chan delegatedMarkReadCall
}

func (f *fakeDelegatedMarkReadApp) DB() *store.DB { return nil }

func (f *fakeDelegatedMarkReadApp) MarkChatReadReceipt(_ context.Context, chat types.JID) error {
	f.calls <- delegatedMarkReadCall{chat: chat, read: true, receipt: true}
	return nil
}

// TestChatsMarkReadDelegatesThroughProductionServerWhenStoreLocked proves the
// delegated mark-read path uses the network receipt (never app-state), and that
// mark-unread is never executed as an in-daemon app-state write: it falls through
// to the local store-lock path and fails fast, like archive/pin/mute.
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

	t.Run("mark-read uses the receipt path", func(t *testing.T) {
		stdout, stderr, err := runPresenceDelegateHelper(t, []string{
			"--store", storeDir, "--json", "--timeout", "750ms",
			"chats", "mark-read", "--chat", "123@s.whatsapp.net",
		})
		if err != nil {
			t.Fatalf("chats mark-read failed: %v stdout=%q stderr=%q", err, stdout, stderr)
		}

		select {
		case call := <-fake.calls:
			if call.chat.String() != "123@s.whatsapp.net" || !call.read || !call.receipt {
				t.Fatalf("fake mark-read call = %+v, want chat 123@s.whatsapp.net read true receipt true", call)
			}
		case <-contextWithTestTimeout(t).Done():
			t.Fatal("timed out waiting for delegated mark-read call")
		}
		if strings.Contains(stderr, "store is locked") {
			t.Fatalf("delegated command returned lock error: stderr=%q", stderr)
		}
		for _, want := range []string{`"ok":true`, `"action":"mark-read"`, `"chat":"123@s.whatsapp.net"`} {
			if !strings.Contains(stdout, want) {
				t.Fatalf("stdout %q missing %s", stdout, want)
			}
		}
	})

	t.Run("mark-unread stays off the daemon", func(t *testing.T) {
		stdout, stderr, err := runPresenceDelegateHelper(t, []string{
			"--store", storeDir, "--json", "--timeout", "750ms",
			"chats", "mark-unread", "--chat", "123@s.whatsapp.net",
		})
		if err == nil {
			t.Fatalf("chats mark-unread succeeded under a running daemon; want fail-fast: stdout=%q stderr=%q", stdout, stderr)
		}
		if !strings.Contains(stderr, "store is locked") {
			t.Fatalf("stderr = %q, want store-lock fail-fast", stderr)
		}
		select {
		case call := <-fake.calls:
			t.Fatalf("mark-unread was executed inside the daemon as an app-state write: %+v", call)
		case <-time.After(300 * time.Millisecond):
		}
	})

	stop()
	stopped = true
	if _, err := os.Lstat(socketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("delegate socket remains after stop: %v", err)
	}
}

// TestExecuteDelegatedMarkReadRefusesUnreadAppStateWrite is the compile-time and
// runtime invariant behind F4: the delegated executor has no app-state method,
// and a mark-unread request is refused before it can touch any store.
func TestExecuteDelegatedMarkReadRefusesUnreadAppStateWrite(t *testing.T) {
	fake := &fakeDelegatedMarkReadApp{calls: make(chan delegatedMarkReadCall, 1)}
	unread := false
	_, err := executeDelegatedMarkRead(context.Background(), fake, sendDelegateRequest{
		Version: sendDelegateVersion,
		Kind:    "mark_read",
		To:      "123@s.whatsapp.net",
		Read:    &unread,
	})
	if err == nil || !strings.Contains(err.Error(), "app-state write") {
		t.Fatalf("error = %v, want app-state refusal", err)
	}
	select {
	case call := <-fake.calls:
		t.Fatalf("mark-unread reached the app-state write: %+v", call)
	default:
	}
}

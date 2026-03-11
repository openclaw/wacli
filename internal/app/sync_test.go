package app

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestSyncStoresLiveAndHistoryMessages(t *testing.T) {
	a := newIPCTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "123", Server: types.DefaultUserServer}
	f.contacts[chat.ToNonAD()] = types.ContactInfo{
		Found:     true,
		FullName:  "Alice",
		FirstName: "Alice",
		PushName:  "Alice",
	}

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	live := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:     chat,
				Sender:   chat,
				IsFromMe: false,
				IsGroup:  false,
			},
			ID:        "m-live",
			Timestamp: base.Add(2 * time.Second),
			PushName:  "Alice",
		},
		Message: &waProto.Message{Conversation: proto.String("hello")},
	}

	histMsg := &waWeb.WebMessageInfo{
		Key: &waCommon.MessageKey{
			RemoteJID: proto.String(chat.String()),
			FromMe:    proto.Bool(false),
			ID:        proto.String("m-hist"),
		},
		MessageTimestamp: proto.Uint64(uint64(base.Add(1 * time.Second).Unix())),
		Message:          &waProto.Message{Conversation: proto.String("older")},
	}
	history := &events.HistorySync{
		Data: &waHistorySync.HistorySync{
			SyncType: waHistorySync.HistorySync_FULL.Enum(),
			Conversations: []*waHistorySync.Conversation{{
				ID:       proto.String(chat.String()),
				Messages: []*waHistorySync.HistorySyncMsg{{Message: histMsg}},
			}},
		},
	}

	f.connectEvents = []interface{}{live, history}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	res, err := a.Sync(ctx, SyncOptions{
		Mode:    SyncModeFollow,
		AllowQR: false,
	})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if res.MessagesStored != 2 {
		t.Fatalf("expected 2 MessagesStored, got %d", res.MessagesStored)
	}
	if n, err := a.db.CountMessages(); err != nil || n != 2 {
		t.Fatalf("expected 2 messages in DB, got %d (err=%v)", n, err)
	}
}

func TestSyncStoresDisplayText(t *testing.T) {
	a := newIPCTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "123", Server: types.DefaultUserServer}
	f.contacts[chat.ToNonAD()] = types.ContactInfo{
		Found:     true,
		FullName:  "Alice",
		FirstName: "Alice",
		PushName:  "Alice",
	}

	base := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)

	textMsg := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:     chat,
				Sender:   chat,
				IsFromMe: false,
				IsGroup:  false,
			},
			ID:        "m-text",
			Timestamp: base.Add(1 * time.Second),
			PushName:  "Alice",
		},
		Message: &waProto.Message{Conversation: proto.String("hello")},
	}

	imageMsg := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:     chat,
				Sender:   chat,
				IsFromMe: false,
				IsGroup:  false,
			},
			ID:        "m-image",
			Timestamp: base.Add(2 * time.Second),
			PushName:  "Alice",
		},
		Message: &waProto.Message{
			ImageMessage: &waProto.ImageMessage{
				Mimetype:      proto.String("image/jpeg"),
				DirectPath:    proto.String("/direct"),
				MediaKey:      []byte{1},
				FileSHA256:    []byte{2},
				FileEncSHA256: []byte{3},
				FileLength:    proto.Uint64(10),
			},
		},
	}

	replyMsg := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:     chat,
				Sender:   chat,
				IsFromMe: false,
				IsGroup:  false,
			},
			ID:        "m-reply",
			Timestamp: base.Add(3 * time.Second),
			PushName:  "Alice",
		},
		Message: &waProto.Message{
			ExtendedTextMessage: &waProto.ExtendedTextMessage{
				Text: proto.String("reply text"),
				ContextInfo: &waProto.ContextInfo{
					StanzaID: proto.String("m-text"),
					QuotedMessage: &waProto.Message{
						Conversation: proto.String("quoted text"),
					},
				},
			},
		},
	}

	reactionMsg := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:     chat,
				Sender:   chat,
				IsFromMe: false,
				IsGroup:  false,
			},
			ID:        "m-react",
			Timestamp: base.Add(4 * time.Second),
			PushName:  "Alice",
		},
		Message: &waProto.Message{
			ReactionMessage: &waProto.ReactionMessage{
				Text: proto.String("👍"),
				Key:  &waProto.MessageKey{ID: proto.String("m-text")},
			},
		},
	}

	f.connectEvents = []interface{}{textMsg, imageMsg, replyMsg, reactionMsg}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	res, err := a.Sync(ctx, SyncOptions{
		Mode:    SyncModeFollow,
		AllowQR: false,
	})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if res.MessagesStored != 4 {
		t.Fatalf("expected 4 MessagesStored, got %d", res.MessagesStored)
	}

	msg, err := a.db.GetMessage(chat.String(), "m-text")
	if err != nil {
		t.Fatalf("GetMessage text: %v", err)
	}
	if msg.DisplayText != "hello" {
		t.Fatalf("expected display text 'hello', got %q", msg.DisplayText)
	}

	msg, err = a.db.GetMessage(chat.String(), "m-image")
	if err != nil {
		t.Fatalf("GetMessage image: %v", err)
	}
	if msg.DisplayText != "Sent image" {
		t.Fatalf("expected display text 'Sent image', got %q", msg.DisplayText)
	}

	msg, err = a.db.GetMessage(chat.String(), "m-reply")
	if err != nil {
		t.Fatalf("GetMessage reply: %v", err)
	}
	if msg.DisplayText != "> quoted text\nreply text" {
		t.Fatalf("unexpected reply display text: %q", msg.DisplayText)
	}

	msg, err = a.db.GetMessage(chat.String(), "m-react")
	if err != nil {
		t.Fatalf("GetMessage react: %v", err)
	}
	if msg.DisplayText != "Reacted 👍 to hello" {
		t.Fatalf("unexpected reaction display text: %q", msg.DisplayText)
	}
}

func TestSyncOnceIdleExit(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	_, err := a.Sync(ctx, SyncOptions{
		Mode:     SyncModeOnce,
		AllowQR:  false,
		IdleExit: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if time.Since(start) > 1500*time.Millisecond {
		t.Fatalf("expected to exit quickly on idle, took %s", time.Since(start))
	}
}

func waitForSocket(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("socket %s did not appear within %v", path, timeout)
}

func TestSyncFollowHostsIPCAndDelegatedSendSucceeds(t *testing.T) {
	a := newIPCTestApp(t)
	fw := newFakeWA()
	a.wa = fw
	sockPath := SendSocketPath(a.opts.StoreDir)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	syncDone := make(chan error, 1)
	go func() {
		_, err := a.Sync(ctx, SyncOptions{
			Mode:    SyncModeFollow,
			AllowQR: false,
		})
		syncDone <- err
	}()

	// Wait for socket to appear.
	waitForSocket(t, sockPath, 2*time.Second)

	// Issue a delegated send.
	res, err := DelegateSendText(context.Background(), a.opts.StoreDir, SendTextParams{
		To:      "6591234567",
		Message: "hello from IPC",
	})
	if err != nil {
		t.Fatalf("DelegateSendText: %v", err)
	}
	if res.ID != "msgid" {
		t.Errorf("ID = %q, want %q", res.ID, "msgid")
	}
	if len(fw.sendTextCalls) != 1 {
		t.Errorf("expected 1 SendText call, got %d", len(fw.sendTextCalls))
	}

	// Cancel sync and wait for exit.
	cancel()
	select {
	case err := <-syncDone:
		if err != nil {
			t.Fatalf("Sync returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Sync did not exit after cancel")
	}

	// Socket should be gone after sync exits.
	if _, err := os.Stat(sockPath); !os.IsNotExist(err) {
		t.Errorf("socket should be removed after sync exit, got err: %v", err)
	}
}

func TestSyncFollowConcurrentDelegatedSends(t *testing.T) {
	a := newIPCTestApp(t)
	fw := newFakeWA()
	a.wa = fw
	sockPath := SendSocketPath(a.opts.StoreDir)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	syncDone := make(chan error, 1)
	go func() {
		_, err := a.Sync(ctx, SyncOptions{
			Mode:    SyncModeFollow,
			AllowQR: false,
		})
		syncDone <- err
	}()

	waitForSocket(t, sockPath, 2*time.Second)

	// Fire 3 concurrent delegated sends.
	const n = 3
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			_, err := DelegateSendText(context.Background(), a.opts.StoreDir, SendTextParams{
				To:      "6591234567",
				Message: fmt.Sprintf("concurrent msg %d", idx),
			})
			errs <- err
		}(i)
	}

	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Errorf("concurrent send %d failed: %v", i, err)
		}
	}

	// All 3 sends should have been processed.
	if got := len(fw.sendTextCalls); got != n {
		t.Errorf("expected %d SendText calls, got %d", n, got)
	}

	cancel()
	select {
	case err := <-syncDone:
		if err != nil {
			t.Fatalf("Sync returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Sync did not exit after cancel")
	}
}

func TestSyncOnceDoesNotHostSocket(t *testing.T) {
	a := newIPCTestApp(t)
	fw := newFakeWA()
	a.wa = fw
	sockPath := SendSocketPath(a.opts.StoreDir)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := a.Sync(ctx, SyncOptions{
		Mode:     SyncModeOnce,
		AllowQR:  false,
		IdleExit: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}

	// Socket should never have been created.
	if _, err := os.Stat(sockPath); err == nil {
		t.Error("socket should not exist for once mode")
	}
}

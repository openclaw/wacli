package app

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steipete/wacli/internal/store"
	"github.com/steipete/wacli/internal/wa"
	"go.mau.fi/whatsmeow/appstate"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestLiveSyncWarnsOnEncryptedReactionDecryptFailure(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "123", Server: types.DefaultUserServer}
	reactionMsg := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:     chat,
				Sender:   chat,
				IsFromMe: false,
				IsGroup:  false,
			},
			ID:        "m-enc-react",
			Timestamp: time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC),
			PushName:  "Alice",
		},
		Message: &waProto.Message{
			EncReactionMessage: &waProto.EncReactionMessage{
				TargetMessageKey: &waCommon.MessageKey{ID: proto.String("m-text")},
			},
		},
	}

	var messagesStored atomic.Int64
	out := captureStderr(t, func() {
		a.handleLiveSyncMessage(context.Background(), SyncOptions{}, reactionMsg, &messagesStored, func(string, string) {}, nil)
	})

	if !strings.Contains(out, "warning: failed to decrypt reaction message m-enc-react: not supported") {
		t.Fatalf("expected encrypted reaction decrypt warning, got:\n%s", out)
	}
	if messagesStored.Load() != 1 {
		t.Fatalf("expected message to still be stored, got %d", messagesStored.Load())
	}
	msg, err := a.db.GetMessage(chat.String(), "m-enc-react")
	if err != nil {
		t.Fatalf("GetMessage encrypted reaction: %v", err)
	}
	if msg.DisplayText != "Reacted to message" {
		t.Fatalf("expected fallback reaction display text, got %q", msg.DisplayText)
	}
}

func TestAppStateLTHashMismatchRequestsRecoveryOnce(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	var recoveries sync.Map
	err := fmt.Errorf("failed to verify patch v5848: %w", appstate.ErrMismatchingLTHash)
	a.handleAppStateSyncError(context.Background(), &events.AppStateSyncError{
		Name:  appstate.WAPatchRegularLow,
		Error: err,
	}, &recoveries)
	a.handleAppStateSyncError(context.Background(), &events.AppStateSyncError{
		Name:  appstate.WAPatchRegularLow,
		Error: err,
	}, &recoveries)

	waitForCondition(t, time.Second, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.appStateRecoveries) == 1
	})
	f.mu.Lock()
	defer f.mu.Unlock()
	if got := f.appStateRecoveries[0]; got != string(appstate.WAPatchRegularLow) {
		t.Fatalf("recovery collection = %q", got)
	}
}

func TestAppStateNonLTHashErrorDoesNotRequestRecovery(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	var recoveries sync.Map
	a.handleAppStateSyncError(context.Background(), &events.AppStateSyncError{
		Name:  appstate.WAPatchRegularLow,
		Error: errors.New("mismatching patch MAC"),
	}, &recoveries)

	time.Sleep(20 * time.Millisecond)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.appStateRecoveries) != 0 {
		t.Fatalf("recovery requests = %v, want none", f.appStateRecoveries)
	}
}

func TestStarEventStoresAndClearsStarredState(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "123", Server: types.DefaultUserServer}
	if err := a.db.UpsertChat(chat.String(), "dm", "Alice", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	msgTime := time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC)
	if err := a.db.UpsertMessage(store.UpsertMessageParams{
		ChatJID:   chat.String(),
		MsgID:     "m-star",
		SenderJID: chat.String(),
		Timestamp: msgTime,
		Text:      "save this",
	}); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}

	starredAt := msgTime.Add(time.Minute)
	a.handleStarEvent(context.Background(), &events.Star{
		ChatJID:   chat,
		SenderJID: chat,
		MessageID: "m-star",
		Timestamp: starredAt,
		Action:    &waSyncAction.StarAction{Starred: proto.Bool(true)},
	})
	msg, err := a.db.GetMessage(chat.String(), "m-star")
	if err != nil {
		t.Fatalf("GetMessage starred: %v", err)
	}
	if !msg.Starred || !msg.StarredAt.Equal(starredAt) {
		t.Fatalf("unexpected starred state: %+v", msg)
	}

	a.handleStarEvent(context.Background(), &events.Star{
		ChatJID:   chat,
		MessageID: "m-star",
		Timestamp: starredAt.Add(time.Minute),
		Action:    &waSyncAction.StarAction{Starred: proto.Bool(false)},
	})
	msg, err = a.db.GetMessage(chat.String(), "m-star")
	if err != nil {
		t.Fatalf("GetMessage unstarred: %v", err)
	}
	if msg.Starred {
		t.Fatalf("expected unstarred message, got %+v", msg)
	}
}

func TestLiveSyncIgnoresHistorySyncProtocolMessage(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	syncType := waE2E.HistorySyncType_INITIAL_BOOTSTRAP
	evt := &events.Message{
		Message: &waProto.Message{
			ProtocolMessage: &waProto.ProtocolMessage{
				HistorySyncNotification: &waE2E.HistorySyncNotification{SyncType: &syncType},
			},
		},
	}

	var messagesStored atomic.Int64
	a.handleLiveSyncMessage(context.Background(), SyncOptions{}, evt, &messagesStored, func(string, string) {}, nil)

	if messagesStored.Load() != 0 {
		t.Fatalf("history sync protocol message stored count = %d, want 0", messagesStored.Load())
	}
	count, err := a.db.CountMessages()
	if err != nil {
		t.Fatalf("CountMessages: %v", err)
	}
	if count != 0 {
		t.Fatalf("db messages = %d, want 0", count)
	}
}

func TestHistorySyncDecryptsEncryptedReaction(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "123", Server: types.DefaultUserServer}
	f.contacts[chat.ToNonAD()] = types.ContactInfo{Found: true, FullName: "Alice"}
	f.decryptedReaction = &waProto.ReactionMessage{
		Text: proto.String("❤️"),
		Key:  &waCommon.MessageKey{ID: proto.String("m-text")},
	}

	base := time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC)
	textMsg := &waWeb.WebMessageInfo{
		Key: &waCommon.MessageKey{
			RemoteJID: proto.String(chat.String()),
			FromMe:    proto.Bool(false),
			ID:        proto.String("m-text"),
		},
		MessageTimestamp: proto.Uint64(uint64(base.Unix())),
		Message:          &waProto.Message{Conversation: proto.String("hello")},
	}
	reactionMsg := &waWeb.WebMessageInfo{
		Key: &waCommon.MessageKey{
			RemoteJID: proto.String(chat.String()),
			FromMe:    proto.Bool(false),
			ID:        proto.String("m-enc-react"),
		},
		MessageTimestamp: proto.Uint64(uint64(base.Add(time.Second).Unix())),
		Message: &waProto.Message{
			EncReactionMessage: &waProto.EncReactionMessage{
				TargetMessageKey: &waCommon.MessageKey{ID: proto.String("m-text")},
			},
		},
	}
	history := &events.HistorySync{
		Data: &waHistorySync.HistorySync{
			SyncType: waHistorySync.HistorySync_FULL.Enum(),
			Conversations: []*waHistorySync.Conversation{{
				ID:       proto.String(chat.String()),
				Messages: []*waHistorySync.HistorySyncMsg{{Message: textMsg}, {Message: reactionMsg}},
			}},
		},
	}

	var messagesStored atomic.Int64
	var lastEvent atomic.Int64
	a.handleHistorySync(context.Background(), SyncOptions{}, history, &messagesStored, &lastEvent, func(string, string) {})

	if messagesStored.Load() != 2 {
		t.Fatalf("expected 2 stored messages, got %d", messagesStored.Load())
	}
	msg, err := a.db.GetMessage(chat.String(), "m-enc-react")
	if err != nil {
		t.Fatalf("GetMessage encrypted reaction: %v", err)
	}
	if msg.DisplayText != "Reacted ❤️ to hello" {
		t.Fatalf("DisplayText = %q, want decrypted reaction display", msg.DisplayText)
	}
	if msg.ReactionToID != "m-text" || msg.ReactionEmoji != "❤️" {
		t.Fatalf("unexpected reaction fields: to=%q emoji=%q", msg.ReactionToID, msg.ReactionEmoji)
	}
}

func TestSyncStoresLiveAndHistoryMessages(t *testing.T) {
	a := newTestApp(t)
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

func TestStoreParsedMessageNormalizesDefaultUserADJIDs(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "123:4", Server: types.DefaultUserServer}
	sender := types.JID{User: "456:7", Server: types.DefaultUserServer}
	f.contacts[chat.ToNonAD()] = types.ContactInfo{Found: true, FullName: "Alice"}
	f.contacts[sender.ToNonAD()] = types.ContactInfo{Found: true, FullName: "Bob"}

	err := a.storeParsedMessage(context.Background(), wa.ParsedMessage{
		Chat:      chat,
		ID:        "m-normalized",
		SenderJID: sender.String(),
		Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		Text:      "hello",
	})
	if err != nil {
		t.Fatalf("storeParsedMessage: %v", err)
	}

	msg, err := a.db.GetMessage(chat.ToNonAD().String(), "m-normalized")
	if err != nil {
		t.Fatalf("GetMessage canonical chat: %v", err)
	}
	if msg.ChatJID != chat.ToNonAD().String() {
		t.Fatalf("ChatJID = %q, want %q", msg.ChatJID, chat.ToNonAD().String())
	}
	wantSender, err := types.ParseJID(sender.String())
	if err != nil {
		t.Fatalf("ParseJID sender: %v", err)
	}
	if msg.SenderJID != wantSender.ToNonAD().String() {
		t.Fatalf("SenderJID = %q, want %q", msg.SenderJID, wantSender.ToNonAD().String())
	}
}

func TestStoreParsedMessageResolvesLIDChatAndSender(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	lid := types.JID{User: "999123456789", Server: types.HiddenUserServer}
	pn := types.JID{User: "15551234567", Server: types.DefaultUserServer}
	f.lids[lid.ToNonAD()] = pn
	f.contacts[pn.ToNonAD()] = types.ContactInfo{Found: true, FullName: "Alice"}

	err := a.storeParsedMessage(context.Background(), wa.ParsedMessage{
		Chat:      lid,
		ID:        "m-lid",
		SenderJID: lid.String(),
		Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		Text:      "hello",
	})
	if err != nil {
		t.Fatalf("storeParsedMessage: %v", err)
	}

	msg, err := a.db.GetMessage(pn.String(), "m-lid")
	if err != nil {
		t.Fatalf("GetMessage resolved chat: %v", err)
	}
	if msg.ChatJID != pn.String() {
		t.Fatalf("ChatJID = %q, want %q", msg.ChatJID, pn.String())
	}
	if msg.SenderJID != pn.String() {
		t.Fatalf("SenderJID = %q, want %q", msg.SenderJID, pn.String())
	}
	if msg.ChatName != "Alice" {
		t.Fatalf("ChatName = %q, want Alice", msg.ChatName)
	}
	if _, err := a.db.GetMessage(lid.String(), "m-lid"); err == nil {
		t.Fatalf("message was also stored under unresolved LID chat")
	}
}

func TestStoreParsedMessageStoresForwardedMetadata(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "123", Server: types.DefaultUserServer}
	err := a.storeParsedMessage(context.Background(), wa.ParsedMessage{
		Chat:            chat,
		ID:              "m-forwarded",
		SenderJID:       chat.String(),
		Timestamp:       time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		Text:            "forwarded",
		IsForwarded:     true,
		ForwardingScore: 4,
	})
	if err != nil {
		t.Fatalf("storeParsedMessage: %v", err)
	}

	msg, err := a.db.GetMessage(chat.String(), "m-forwarded")
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if !msg.IsForwarded {
		t.Fatalf("expected forwarded message, got %+v", msg)
	}
	if msg.ForwardingScore != 4 {
		t.Fatalf("ForwardingScore = %d, want 4", msg.ForwardingScore)
	}
}

func TestSyncStoresDisplayText(t *testing.T) {
	a := newTestApp(t)
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
	if msg.ReactionToID != "m-text" || msg.ReactionEmoji != "👍" {
		t.Fatalf("unexpected reaction fields: to=%q emoji=%q", msg.ReactionToID, msg.ReactionEmoji)
	}
}

func TestSyncMediaEnqueueUsesBoundedBackpressure(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	f.downloadDelay = 5 * time.Millisecond

	chat := types.JID{User: "123", Server: types.DefaultUserServer}
	f.contacts[chat.ToNonAD()] = types.ContactInfo{
		Found:    true,
		FullName: "Alice",
		PushName: "Alice",
	}

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 600; i++ {
		f.connectEvents = append(f.connectEvents, &events.Message{
			Info: types.MessageInfo{
				MessageSource: types.MessageSource{
					Chat:     chat,
					Sender:   chat,
					IsFromMe: false,
				},
				ID:        fmt.Sprintf("media-%03d", i),
				Timestamp: base.Add(time.Duration(i) * time.Second),
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
		})
	}

	before := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var during int
	res, err := a.Sync(ctx, SyncOptions{
		Mode:          SyncModeFollow,
		AllowQR:       false,
		DownloadMedia: true,
		AfterConnect: func(context.Context) error {
			during = runtime.NumGoroutine()
			cancel()
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if res.MessagesStored != 600 {
		t.Fatalf("expected 600 messages stored, got %d", res.MessagesStored)
	}
	if leaked := during - before; leaked > 20 {
		t.Fatalf("expected bounded media enqueue goroutines, saw +%d (before=%d during=%d)", leaked, before, during)
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

func TestSyncOnceIdleExitIgnoresNonMessageEvents(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go func() {
		ticker := time.NewTicker(30 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				f.emit(&events.Connected{})
			}
		}
	}()

	start := time.Now()
	_, err := a.Sync(ctx, SyncOptions{
		Mode:     SyncModeOnce,
		AllowQR:  false,
		IdleExit: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Fatalf("expected non-message events not to reset idle timer, took %s", elapsed)
	}
}

func TestSyncOnceIdleExitStartsAfterConnected(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	f.connectDelay = 400 * time.Millisecond
	a.wa = f

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	_, err := a.Sync(ctx, SyncOptions{
		Mode:     SyncModeOnce,
		AllowQR:  false,
		IdleExit: 600 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if elapsed := time.Since(start); elapsed < f.connectDelay+600*time.Millisecond {
		t.Fatalf("expected idle timer to start after connect, exited after %s", elapsed)
	}
}

func TestSyncRetriesTransientAuthConnectFailure(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	f.authed = false
	f.connectErrs = []error{fmt.Errorf("QR code timed out; run `wacli auth` again")}
	a.wa = f

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err := a.Sync(ctx, SyncOptions{
		Mode:     SyncModeOnce,
		AllowQR:  true,
		IdleExit: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if f.connectCalls != 2 {
		t.Fatalf("connect calls = %d, want 2", f.connectCalls)
	}
}

func TestSyncDoesNotRetryTransientConnectFailureOutsideAuthFlow(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	f.connectErrs = []error{fmt.Errorf("QR code timed out; run `wacli auth` again")}
	a.wa = f

	_, err := a.Sync(context.Background(), SyncOptions{
		Mode:    SyncModeOnce,
		AllowQR: false,
	})
	if err == nil {
		t.Fatalf("expected connect error")
	}
	if f.connectCalls != 1 {
		t.Fatalf("connect calls = %d, want 1", f.connectCalls)
	}
}

func waitForCondition(t *testing.T, timeout time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ok() {
		t.Fatalf("condition not met within %s", timeout)
	}
}

func TestSyncDoesNotRetryNonTransientAuthConnectFailure(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	f.authed = false
	f.connectErrs = []error{fmt.Errorf("QR pairing failed: bad code")}
	a.wa = f

	_, err := a.Sync(context.Background(), SyncOptions{
		Mode:    SyncModeOnce,
		AllowQR: true,
	})
	if err == nil || !strings.Contains(err.Error(), "bad code") {
		t.Fatalf("expected pairing error, got %v", err)
	}
	if f.connectCalls != 1 {
		t.Fatalf("connect calls = %d, want 1", f.connectCalls)
	}
}

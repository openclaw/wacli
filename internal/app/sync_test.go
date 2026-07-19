package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow/appstate"
	waBinary "go.mau.fi/whatsmeow/binary"
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

func TestLiveSyncIncrementsUnreadCountForIncomingMessages(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "123", Server: types.DefaultUserServer}
	incoming := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:     chat,
				Sender:   chat,
				IsFromMe: false,
			},
			ID:        "incoming-1",
			Timestamp: time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC),
			PushName:  "Alice",
		},
		Message: &waProto.Message{Conversation: proto.String("hello")},
	}

	var messagesStored atomic.Int64
	a.handleLiveSyncMessage(context.Background(), SyncOptions{}, incoming, &messagesStored, func(string, string) {}, nil)

	c, err := a.db.GetChat(chat.String())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if !c.Unread || c.UnreadCount != 1 {
		t.Fatalf("unread state after incoming message = %+v, want count 1", c)
	}

	a.handleLiveSyncMessage(context.Background(), SyncOptions{}, incoming, &messagesStored, func(string, string) {}, nil)
	c, err = a.db.GetChat(chat.String())
	if err != nil {
		t.Fatalf("GetChat after duplicate: %v", err)
	}
	if !c.Unread || c.UnreadCount != 1 {
		t.Fatalf("unread state after duplicate incoming message = %+v, want count 1", c)
	}
}

func TestSyncEventHandlerClearsUnreadCountOnReadSelfReceipt(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "123", Server: types.DefaultUserServer}
	if err := a.db.SetChatUnreadCount(chat.String(), 3); err != nil {
		t.Fatalf("seed unread count: %v", err)
	}

	var messagesStored atomic.Int64
	var lastEvent atomic.Int64
	a.addSyncEventHandler(
		context.Background(),
		SyncOptions{},
		&messagesStored,
		&lastEvent,
		make(chan struct{}, 1),
		make(chan staleReconnectRequest, 1),
		func(string, string) {},
		nil,
		nil,
		&syncPresence{},
		nil,
	)
	f.emit(&events.Receipt{
		MessageSource: types.MessageSource{Chat: chat},
		MessageIDs:    []types.MessageID{"incoming-1", "incoming-2"},
		Type:          types.ReceiptTypeReadSelf,
	})

	c, err := a.db.GetChat(chat.String())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if c.Unread || c.UnreadCount != 0 {
		t.Fatalf("unread state after read-self receipt = %+v, want clear", c)
	}
	if lastEvent.Load() == 0 {
		t.Fatal("read-self receipt did not update last event timestamp")
	}
}

func TestSyncEventHandlerIgnoresRegularReadReceiptsForUnreadCount(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "123", Server: types.DefaultUserServer}
	if err := a.db.SetChatUnreadCount(chat.String(), 3); err != nil {
		t.Fatalf("seed unread count: %v", err)
	}

	var messagesStored atomic.Int64
	var lastEvent atomic.Int64
	a.addSyncEventHandler(
		context.Background(),
		SyncOptions{},
		&messagesStored,
		&lastEvent,
		make(chan struct{}, 1),
		make(chan staleReconnectRequest, 1),
		func(string, string) {},
		nil,
		nil,
		&syncPresence{},
		nil,
	)
	f.emit(&events.Receipt{
		MessageSource: types.MessageSource{Chat: chat},
		MessageIDs:    []types.MessageID{"incoming-1"},
		Type:          types.ReceiptTypeRead,
	})

	c, err := a.db.GetChat(chat.String())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if !c.Unread || c.UnreadCount != 3 {
		t.Fatalf("unread state after regular read receipt = %+v, want unchanged count 3", c)
	}
}

func TestLiveSyncDoesNotIncrementUnreadForOwnMessagesOrStatus(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "123", Server: types.DefaultUserServer}
	own := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:     chat,
				Sender:   chat,
				IsFromMe: true,
			},
			ID:        "own-1",
			Timestamp: time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC),
		},
		Message: &waProto.Message{Conversation: proto.String("sent")},
	}
	status := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:     types.StatusBroadcastJID,
				Sender:   chat,
				IsFromMe: false,
			},
			ID:        "status-1",
			Timestamp: time.Date(2024, 1, 3, 0, 1, 0, 0, time.UTC),
		},
		Message: &waProto.Message{Conversation: proto.String("status")},
	}

	var messagesStored atomic.Int64
	a.handleLiveSyncMessage(context.Background(), SyncOptions{}, own, &messagesStored, func(string, string) {}, nil)
	a.handleLiveSyncMessage(context.Background(), SyncOptions{}, status, &messagesStored, func(string, string) {}, nil)

	c, err := a.db.GetChat(chat.String())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if c.Unread || c.UnreadCount != 0 {
		t.Fatalf("unread state after own message = %+v, want count 0", c)
	}
	if _, err := a.db.GetChat(types.StatusBroadcastJID.String()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("status chat lookup err = %v, want sql.ErrNoRows", err)
	}
}

func TestHistorySyncStoresConversationUnreadCount(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "123", Server: types.DefaultUserServer}
	base := time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC)
	msg := &waWeb.WebMessageInfo{
		Key: &waCommon.MessageKey{
			RemoteJID: proto.String(chat.String()),
			FromMe:    proto.Bool(false),
			ID:        proto.String("history-1"),
		},
		MessageTimestamp: proto.Uint64(uint64(base.Unix())),
		Message:          &waProto.Message{Conversation: proto.String("hello")},
	}
	history := &events.HistorySync{Data: &waHistorySync.HistorySync{
		SyncType: waHistorySync.HistorySync_FULL.Enum(),
		Conversations: []*waHistorySync.Conversation{{
			ID:          proto.String(chat.String()),
			UnreadCount: proto.Uint32(3),
			Messages:    []*waHistorySync.HistorySyncMsg{{Message: msg}},
		}},
	}}

	var messagesStored atomic.Int64
	var lastEvent atomic.Int64
	a.handleHistorySync(context.Background(), SyncOptions{}, history, &messagesStored, &lastEvent, func(string, string) {})

	c, err := a.db.GetChat(chat.String())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if !c.Unread || c.UnreadCount != 3 {
		t.Fatalf("unread state after history sync = %+v, want count 3", c)
	}
}

func TestHistorySyncStoresDeviceSentMessagesInDestinationChat(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	self := types.NewJID("1111111111", types.DefaultUserServer)
	dest := types.NewJID("15551234567", types.DefaultUserServer)
	base := time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC)
	msg := &waWeb.WebMessageInfo{
		Key: &waCommon.MessageKey{
			RemoteJID: proto.String(self.String()),
			FromMe:    proto.Bool(false),
			ID:        proto.String("device-sent-1"),
		},
		MessageTimestamp: proto.Uint64(uint64(base.Unix())),
		Message: &waProto.Message{
			DeviceSentMessage: &waProto.DeviceSentMessage{
				DestinationJID: proto.String(dest.String()),
				Message:        &waProto.Message{Conversation: proto.String("sent from phone")},
			},
		},
	}
	history := &events.HistorySync{Data: &waHistorySync.HistorySync{
		SyncType: waHistorySync.HistorySync_FULL.Enum(),
		Conversations: []*waHistorySync.Conversation{{
			ID:       proto.String(self.String()),
			Messages: []*waHistorySync.HistorySyncMsg{{Message: msg}},
		}},
	}}

	var messagesStored atomic.Int64
	var lastEvent atomic.Int64
	a.handleHistorySync(context.Background(), SyncOptions{}, history, &messagesStored, &lastEvent, func(string, string) {})

	stored, err := a.db.GetMessage(dest.String(), "device-sent-1")
	if err != nil {
		t.Fatalf("GetMessage destination: %v", err)
	}
	if !stored.FromMe || stored.Text != "sent from phone" || stored.ChatJID != dest.String() {
		t.Fatalf("unexpected stored sent message: %+v", stored)
	}
	if _, err := a.db.GetMessage(self.String(), "device-sent-1"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("self-chat message lookup err = %v, want sql.ErrNoRows", err)
	}
}

func TestHistorySyncStoresMarkedUnreadWithoutCountAsMarkerOnly(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "123", Server: types.DefaultUserServer}
	history := &events.HistorySync{Data: &waHistorySync.HistorySync{
		SyncType: waHistorySync.HistorySync_FULL.Enum(),
		Conversations: []*waHistorySync.Conversation{{
			ID:             proto.String(chat.String()),
			MarkedAsUnread: proto.Bool(true),
		}},
	}}

	var messagesStored atomic.Int64
	var lastEvent atomic.Int64
	a.handleHistorySync(context.Background(), SyncOptions{}, history, &messagesStored, &lastEvent, func(string, string) {})

	c, err := a.db.GetChat(chat.String())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if !c.Unread || c.UnreadCount != 0 {
		t.Fatalf("unread state after marked unread history sync = %+v, want marker-only unread", c)
	}
}

func TestLiveCallOfferStoresCallEvent(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	remote := types.NewJID("15551234567", types.DefaultUserServer)
	when := time.Date(2024, 1, 3, 12, 0, 0, 0, time.UTC)
	a.handleLiveCallEvent(context.Background(), &events.CallOffer{
		BasicCallMeta: types.BasicCallMeta{
			From:        remote,
			CallCreator: remote,
			CallID:      "call-live-1",
			Timestamp:   when,
		},
		Data: &waBinary.Node{Attrs: waBinary.Attrs{"media": "video"}},
	})

	calls, err := a.db.ListCallEvents(store.ListCallEventsParams{ChatJID: remote.String(), Limit: 10})
	if err != nil {
		t.Fatalf("ListCallEvents: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("calls len = %d, want 1", len(calls))
	}
	got := calls[0]
	if got.CallID != "call-live-1" || got.EventType != "offer" || got.Direction != "inbound" || got.Media != "video" {
		t.Fatalf("unexpected call event: %+v", got)
	}
}

func TestLiveCallOfferUsesPNIdentityWhenLinkedLIDExists(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	f.linkedLID = types.NewJID("999123456789", types.HiddenUserServer).String()
	a.wa = f

	self, err := types.ParseJID(f.LinkedJID())
	if err != nil {
		t.Fatalf("ParseJID linked: %v", err)
	}
	remote := types.NewJID("15551234567", types.DefaultUserServer)
	when := time.Date(2024, 1, 3, 12, 0, 0, 0, time.UTC)
	a.handleLiveCallEvent(context.Background(), &events.CallOffer{
		BasicCallMeta: types.BasicCallMeta{
			From:        remote,
			CallCreator: self,
			CallID:      "call-live-lid-1",
			Timestamp:   when,
		},
		Data: &waBinary.Node{Attrs: waBinary.Attrs{"media": "audio"}},
	})

	calls, err := a.db.ListCallEvents(store.ListCallEventsParams{ChatJID: remote.String(), Limit: 10})
	if err != nil {
		t.Fatalf("ListCallEvents: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("calls len = %d, want 1", len(calls))
	}
	got := calls[0]
	if got.CallID != "call-live-lid-1" || got.EventType != "offer" || got.Direction != "outbound" || got.Media != "audio" {
		t.Fatalf("unexpected call event: %+v", got)
	}
}

func TestLiveSyncStoresCallLogMessageAndCallEvent(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.NewJID("15551234567", types.DefaultUserServer)
	outcome := waProto.CallLogMessage_CONNECTED
	callType := waProto.CallLogMessage_REGULAR
	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:     chat,
				Sender:   chat,
				IsFromMe: true,
			},
			ID:        "call-msg-1",
			Timestamp: time.Date(2024, 1, 3, 12, 0, 0, 0, time.UTC),
		},
		Message: &waProto.Message{
			CallLogMesssage: &waProto.CallLogMessage{
				IsVideo:      proto.Bool(false),
				CallOutcome:  &outcome,
				DurationSecs: proto.Int64(61),
				CallType:     &callType,
			},
		},
	}

	var messagesStored atomic.Int64
	a.handleLiveSyncMessage(context.Background(), SyncOptions{}, evt, &messagesStored, func(string, string) {}, nil)

	msg, err := a.db.GetMessage(chat.String(), "call-msg-1")
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if msg.DisplayText != "WhatsApp audio call connected (1m01s)" {
		t.Fatalf("display text = %q", msg.DisplayText)
	}
	calls, err := a.db.ListCallEvents(store.ListCallEventsParams{ChatJID: chat.String(), Limit: 10})
	if err != nil {
		t.Fatalf("ListCallEvents: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("calls len = %d, want 1", len(calls))
	}
	got := calls[0]
	if got.CallID != "call-msg-1" || got.MsgID != "call-msg-1" || got.EventType != "call_log" || got.Direction != "outbound" || got.Outcome != "connected" {
		t.Fatalf("unexpected call log event: %+v", got)
	}
}

func TestHistorySyncStoresCallLogRecords(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.NewJID("15551234567", types.DefaultUserServer)
	when := time.Date(2024, 1, 3, 12, 0, 0, 0, time.UTC)
	result := waSyncAction.CallLogRecord_CONNECTED
	callType := waSyncAction.CallLogRecord_REGULAR
	history := &events.HistorySync{Data: &waHistorySync.HistorySync{
		SyncType: waHistorySync.HistorySync_FULL.Enum(),
		CallLogRecords: []*waSyncAction.CallLogRecord{{
			CallID:         proto.String("call-history-1"),
			CallCreatorJID: proto.String(f.LinkedJID()),
			Participants: []*waSyncAction.CallLogRecord_ParticipantInfo{{
				UserJID:    proto.String(chat.String()),
				CallResult: &result,
			}},
			CallResult: &result,
			CallType:   &callType,
			Duration:   proto.Int64(61),
			StartTime:  proto.Int64(when.UnixMilli()),
			IsIncoming: proto.Bool(false),
			IsVideo:    proto.Bool(false),
		}},
	}}
	var messagesStored atomic.Int64
	var lastEvent atomic.Int64

	a.handleHistorySync(context.Background(), SyncOptions{}, history, &messagesStored, &lastEvent, func(string, string) {})

	if messagesStored.Load() != 0 {
		t.Fatalf("messages stored = %d, want 0", messagesStored.Load())
	}
	calls, err := a.db.ListCallEvents(store.ListCallEventsParams{ChatJID: chat.String(), Limit: 10})
	if err != nil {
		t.Fatalf("ListCallEvents: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("calls len = %d, want 1", len(calls))
	}
	got := calls[0]
	if got.CallID != "call-history-1" || got.EventType != "call_log" || got.Direction != "outbound" || got.Outcome != "connected" || got.DurationSecs != 61 {
		t.Fatalf("unexpected call log event: %+v", got)
	}
}

func TestAppStateCallLogDeleteRemovesStoredCallEvent(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.NewJID("15551234567", types.DefaultUserServer)
	when := time.Date(2024, 1, 3, 12, 0, 0, 0, time.UTC)
	if err := a.db.UpsertChat(chat.String(), "dm", "Alice", when); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := a.db.UpsertCallEvent(store.UpsertCallEventParams{
		ChatJID:   chat.String(),
		CallID:    "call-log-1",
		EventType: "call_log",
		Direction: "outbound",
		Timestamp: when,
	}); err != nil {
		t.Fatalf("UpsertCallEvent: %v", err)
	}

	a.handleLiveCallEvent(context.Background(), &events.AppState{
		SyncActionValue: &waSyncAction.SyncActionValue{
			DeleteIndividualCallLog: &waSyncAction.DeleteIndividualCallLogAction{
				PeerJID:    proto.String(chat.String()),
				IsIncoming: proto.Bool(false),
			},
		},
	})

	calls, err := a.db.ListCallEvents(store.ListCallEventsParams{ChatJID: chat.String(), Limit: 10})
	if err != nil {
		t.Fatalf("ListCallEvents: %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("calls len = %d, want 0: %+v", len(calls), calls)
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

func TestDeleteForMeEventMarksMessageDeletedForCurrentUser(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.NewJID("15551234567", types.DefaultUserServer)
	base := time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC)
	if err := a.db.UpsertChat(chat.String(), "dm", "Alice", base); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := a.db.UpsertMessage(store.UpsertMessageParams{
		ChatJID:     chat.String(),
		MsgID:       "m-delete-for-me",
		SenderJID:   chat.String(),
		Timestamp:   base,
		DisplayText: "secret local copy",
		Text:        "secret local copy",
	}); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}

	a.handleDeleteForMeEvent(context.Background(), &events.DeleteForMe{
		ChatJID:   chat,
		MessageID: "m-delete-for-me",
		Timestamp: base.Add(time.Minute),
		IsFromMe:  false,
	})

	msg, err := a.db.GetMessage(chat.String(), "m-delete-for-me")
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if msg.Revoked || !msg.DeletedForMe {
		t.Fatalf("flags revoked=%v deleted_for_me=%v", msg.Revoked, msg.DeletedForMe)
	}
	if msg.Text != "secret local copy" || msg.DisplayText != "secret local copy" {
		t.Fatalf("text=%q display=%q", msg.Text, msg.DisplayText)
	}
	wantDeletedAt := base.Add(time.Minute)
	if msg.DeletedAt == nil || !msg.DeletedAt.Equal(wantDeletedAt) {
		t.Fatalf("deleted_at=%v want %v", msg.DeletedAt, wantDeletedAt)
	}
	if msg.DeletionReason != store.MessageDeletionReasonWhatsAppDeleteForMe {
		t.Fatalf("deletion_reason=%q", msg.DeletionReason)
	}
	listed, err := a.db.ListMessages(store.ListMessagesParams{ChatJID: chat.String(), Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(listed) != 0 {
		t.Fatalf("deleted-for-me message listed: %+v", listed)
	}
}

func TestSyncFetchesChatAppStateDeltasAfterConnect(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.NewJID("15551234567", types.DefaultUserServer)
	base := time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC)
	result := waSyncAction.CallLogRecord_CONNECTED
	callType := waSyncAction.CallLogRecord_REGULAR
	if err := a.db.UpsertChat(chat.String(), "dm", "Alice", base); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := a.db.UpsertMessage(store.UpsertMessageParams{
		ChatJID:   chat.String(),
		MsgID:     "m-offline-delete-for-me",
		SenderJID: chat.String(),
		Timestamp: base,
		Text:      "gone locally",
	}); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}
	f.appStateFetchEvent = func(name string, fullSync, onlyIfNotSynced bool) interface{} {
		if onlyIfNotSynced {
			return nil
		}
		if name == string(appstate.WAPatchRegularHigh) {
			if fullSync {
				return nil
			}
			return &events.DeleteForMe{
				ChatJID:   chat,
				MessageID: "m-offline-delete-for-me",
				Timestamp: base.Add(time.Minute),
				IsFromMe:  false,
			}
		}
		if name == string(appstate.WAPatchRegularLow) {
			if fullSync {
				return nil
			}
			return &events.Archive{
				JID:       chat,
				Timestamp: base.Add(2 * time.Minute),
				Action:    &waSyncAction.ArchiveChatAction{Archived: proto.Bool(true)},
			}
		}
		if name == string(appstate.WAPatchRegular) {
			if !fullSync {
				return nil
			}
			return &events.AppState{
				SyncActionValue: &waSyncAction.SyncActionValue{
					Timestamp: proto.Int64(base.Add(3 * time.Minute).UnixMilli()),
					CallLogAction: &waSyncAction.CallLogAction{CallLogRecord: &waSyncAction.CallLogRecord{
						CallID:         proto.String("call-app-state-1"),
						CallCreatorJID: proto.String(f.LinkedJID()),
						Participants: []*waSyncAction.CallLogRecord_ParticipantInfo{{
							UserJID:    proto.String(chat.String()),
							CallResult: &result,
						}},
						CallResult: &result,
						CallType:   &callType,
						Duration:   proto.Int64(61),
						StartTime:  proto.Int64(base.Add(3 * time.Minute).UnixMilli()),
						IsIncoming: proto.Bool(false),
						IsVideo:    proto.Bool(false),
					}},
				},
			}
		}
		return nil
	}

	res, err := a.Sync(context.Background(), SyncOptions{
		Mode:     SyncModeOnce,
		IdleExit: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if res.MessagesStored != 0 {
		t.Fatalf("messages stored = %d, want 0", res.MessagesStored)
	}
	f.mu.Lock()
	fetches := append([]fakeAppStateFetch(nil), f.appStateFetches...)
	f.mu.Unlock()
	if len(fetches) != 3 {
		t.Fatalf("app state fetches = %+v", fetches)
	}
	if fetches[0].name != string(appstate.WAPatchRegularHigh) || fetches[0].fullSync || fetches[0].onlyIfNotSynced {
		t.Fatalf("first app state fetch = %+v", fetches[0])
	}
	if fetches[1].name != string(appstate.WAPatchRegularLow) || fetches[1].fullSync || fetches[1].onlyIfNotSynced {
		t.Fatalf("second app state fetch = %+v", fetches[1])
	}
	if fetches[2].name != string(appstate.WAPatchRegular) || !fetches[2].fullSync || fetches[2].onlyIfNotSynced {
		t.Fatalf("third app state fetch = %+v", fetches[2])
	}
	msg, err := a.db.GetMessage(chat.String(), "m-offline-delete-for-me")
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if !msg.DeletedForMe {
		t.Fatalf("message was not marked deleted for me: %+v", msg)
	}
	storedChat, err := a.db.GetChat(chat.String())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if !storedChat.Archived {
		t.Fatalf("chat was not marked archived from regular_low app state: %+v", storedChat)
	}
	calls, err := a.db.ListCallEvents(store.ListCallEventsParams{ChatJID: chat.String(), Limit: 10})
	if err != nil {
		t.Fatalf("ListCallEvents: %v", err)
	}
	if len(calls) != 1 || calls[0].CallID != "call-app-state-1" || calls[0].DurationSecs != 61 {
		t.Fatalf("call log was not stored from regular app state: %+v", calls)
	}
}

func TestChatStateEventsUpdateLocalStore(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "456", Server: types.DefaultUserServer}
	if err := a.db.UpsertChat(chat.String(), "dm", "Bob", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}

	a.handleChatStateEvent(context.Background(), &events.Archive{
		JID:    chat,
		Action: &waSyncAction.ArchiveChatAction{Archived: proto.Bool(true)},
	})
	a.handleChatStateEvent(context.Background(), &events.Pin{
		JID:    chat,
		Action: &waSyncAction.PinAction{Pinned: proto.Bool(true)},
	})
	a.handleChatStateEvent(context.Background(), &events.Mute{
		JID:    chat,
		Action: &waSyncAction.MuteAction{Muted: proto.Bool(true), MuteEndTimestamp: proto.Int64(-1)},
	})
	a.handleChatStateEvent(context.Background(), &events.MarkChatAsRead{
		JID:    chat,
		Action: &waSyncAction.MarkChatAsReadAction{Read: proto.Bool(false)},
	})

	c, err := a.db.GetChat(chat.String())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if !c.Archived || !c.Pinned || c.MutedUntil != -1 || !c.Unread || c.UnreadCount != 0 {
		t.Fatalf("chat state = %+v", c)
	}
}

func TestChatStatePersistenceHandlerCoversOtherCollectionDuringWrite(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	target := types.JID{User: "456", Server: types.DefaultUserServer}
	remoteMute := types.JID{User: "789", Server: types.DefaultUserServer}
	for _, chat := range []types.JID{target, remoteMute} {
		if err := a.db.UpsertChat(chat.String(), "dm", "Bob", time.Now()); err != nil {
			t.Fatalf("UpsertChat: %v", err)
		}
	}
	f.connectEvents = []interface{}{&events.Mute{
		JID:    remoteMute,
		Action: &waSyncAction.MuteAction{Muted: proto.Bool(true), MuteEndTimestamp: proto.Int64(-1)},
	}}

	removeHandler, err := a.AddChatStatePersistenceHandler(context.Background())
	if err != nil {
		t.Fatalf("AddChatStatePersistenceHandler: %v", err)
	}
	if err := a.Connect(context.Background(), false, nil); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := a.ArchiveChat(context.Background(), target, true); err != nil {
		t.Fatalf("ArchiveChat: %v", err)
	}
	removeHandler()
	if err := a.appStatePersist.waitIdle(context.Background()); err != nil {
		t.Fatalf("waitIdle: %v", err)
	}

	stored, err := a.db.GetChat(remoteMute.String())
	if err != nil {
		t.Fatalf("GetChat remote mute: %v", err)
	}
	if stored.MutedUntil != -1 {
		t.Fatalf("connect-time regular_high event was not persisted: %+v", stored)
	}
	stored, err = a.db.GetChat(target.String())
	if err != nil {
		t.Fatalf("GetChat archive target: %v", err)
	}
	if !stored.Archived {
		t.Fatalf("regular_low write was not persisted: %+v", stored)
	}
	required, err := a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularHigh))
	if err != nil {
		t.Fatalf("AppStateRecoveryRequired: %v", err)
	}
	if required {
		t.Fatal("successful connect-time persistence left replay debt")
	}
	f.mu.Lock()
	handlerCount := len(f.handlers)
	f.mu.Unlock()
	if handlerCount != 0 {
		t.Fatalf("event handlers after cleanup = %d, want 0", handlerCount)
	}
}

func TestArchiveChatPNUsesFreshKeylessMessageRange(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "456", Server: types.DefaultUserServer}
	localMessageTS := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	remoteReactionTS := localMessageTS.Add(time.Hour)
	archiveTS := remoteReactionTS.Add(time.Hour)
	previousNowUTC := nowUTC
	nowUTC = func() time.Time { return archiveTS }
	t.Cleanup(func() { nowUTC = previousNowUTC })

	if err := a.db.UpsertChat(chat.String(), "dm", "Bob", localMessageTS); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := a.db.UpsertMessage(store.UpsertMessageParams{
		ChatJID:   chat.String(),
		MsgID:     "latest",
		SenderJID: chat.String(),
		Timestamp: localMessageTS,
		FromMe:    true,
		Text:      "hi",
	}); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}
	// remoteReactionTS intentionally has no local row, matching a linked client
	// that knows about a newer reaction than wacli's message store.

	if err := a.ArchiveChat(context.Background(), chat, true); err != nil {
		t.Fatalf("ArchiveChat: %v", err)
	}

	f.mu.Lock()
	calls := append([]fakeArchiveCall(nil), f.archiveCalls...)
	f.mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("archive calls = %d", len(calls))
	}
	if calls[0].target != chat || !calls[0].archive {
		t.Fatalf("archive call = %+v", calls[0])
	}
	if !calls[0].lastMsgTS.Equal(archiveTS) || !calls[0].lastMsgTS.After(remoteReactionTS) {
		t.Fatalf("lastMsgTS = %s, want fresh range at %s", calls[0].lastMsgTS, archiveTS)
	}
	if calls[0].lastMsgKey != nil {
		t.Fatalf("lastMsgKey = %+v, want nil for incomplete local history", calls[0].lastMsgKey)
	}
	c, err := a.db.GetChat(chat.String())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if !c.Archived {
		t.Fatalf("expected local archived state, got %+v", c)
	}
}

func TestArchiveChatLIDUsesFreshKeylessMessageRangeWithPNStore(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	pn := types.JID{User: "456", Server: types.DefaultUserServer}
	lid := types.JID{User: "999", Server: types.HiddenUserServer}
	f.lids[lid] = pn
	localMessageTS := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	archiveTS := localMessageTS.Add(2 * time.Hour)
	previousNowUTC := nowUTC
	nowUTC = func() time.Time { return archiveTS }
	t.Cleanup(func() { nowUTC = previousNowUTC })

	if err := a.db.UpsertChat(pn.String(), "dm", "Bob", localMessageTS); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := a.db.UpsertMessage(store.UpsertMessageParams{
		ChatJID:   pn.String(),
		MsgID:     "latest-pn",
		SenderJID: pn.String(),
		Timestamp: localMessageTS,
		Text:      "hi",
	}); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}

	if err := a.ArchiveChat(context.Background(), lid, true); err != nil {
		t.Fatalf("ArchiveChat: %v", err)
	}

	f.mu.Lock()
	calls := append([]fakeArchiveCall(nil), f.archiveCalls...)
	f.mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("archive calls = %d", len(calls))
	}
	if calls[0].target != lid {
		t.Fatalf("archive target = %s, want requested LID %s", calls[0].target, lid)
	}
	if !calls[0].lastMsgTS.Equal(archiveTS) || calls[0].lastMsgKey != nil {
		t.Fatalf("archive range = (%s, %+v), want (%s, nil)", calls[0].lastMsgTS, calls[0].lastMsgKey, archiveTS)
	}
	stored, err := a.db.GetChat(pn.String())
	if err != nil {
		t.Fatalf("GetChat PN: %v", err)
	}
	if !stored.Archived {
		t.Fatalf("expected canonical PN chat to be archived, got %+v", stored)
	}
}

func TestMarkChatReadUsesLatestMessageRange(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "456", Server: types.DefaultUserServer}
	when := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	if err := a.db.UpsertChat(chat.String(), "dm", "Bob", when); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := a.db.UpsertMessage(store.UpsertMessageParams{
		ChatJID:   chat.String(),
		MsgID:     "latest",
		SenderJID: chat.String(),
		Timestamp: when,
		FromMe:    true,
		Text:      "hi",
	}); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}

	if err := a.MarkChatRead(context.Background(), chat, true); err != nil {
		t.Fatalf("MarkChatRead: %v", err)
	}

	f.mu.Lock()
	calls := append([]fakeMarkReadCall(nil), f.markReadCalls...)
	f.mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("mark-read calls = %d", len(calls))
	}
	if !calls[0].lastMsgTS.Equal(when) {
		t.Fatalf("lastMsgTS = %s, want %s", calls[0].lastMsgTS, when)
	}
	if calls[0].lastMsgKey == nil || calls[0].lastMsgKey.GetID() != "latest" || !calls[0].lastMsgKey.GetFromMe() {
		t.Fatalf("lastMsgKey = %+v", calls[0].lastMsgKey)
	}
}

func TestMuteChatRecoversRegularHighBeforeWrite(t *testing.T) {
	for _, tc := range []struct {
		name string
		mute bool
	}{
		{name: "mute", mute: true},
		{name: "unmute", mute: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestApp(t)
			f := newFakeWA()
			a.wa = f

			chat := types.JID{User: "456", Server: types.DefaultUserServer}
			if err := a.db.UpsertChat(chat.String(), "dm", "Bob", time.Now()); err != nil {
				t.Fatalf("UpsertChat: %v", err)
			}
			f.appStateFetchErrs = []error{
				fmt.Errorf("failed to verify regular_high patch: %w", appstate.ErrMismatchingLTHash),
				nil,
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := a.MuteChat(ctx, chat, tc.mute, 0); err != nil {
				t.Fatalf("MuteChat: %v", err)
			}

			f.mu.Lock()
			fetches := append([]fakeAppStateFetch(nil), f.appStateFetches...)
			recoveries := append([]string(nil), f.appStateRecoveries...)
			muteCalls := append([]fakeMuteCall(nil), f.muteCalls...)
			f.mu.Unlock()
			if len(fetches) != 2 {
				t.Fatalf("app state fetches = %+v, want delta then full replay", fetches)
			}
			if fetch := fetches[0]; fetch.name != string(appstate.WAPatchRegularHigh) || fetch.fullSync || fetch.onlyIfNotSynced {
				t.Fatalf("app state fetch = %+v, want regular_high delta fetch", fetch)
			}
			if fetch := fetches[1]; fetch.name != string(appstate.WAPatchRegularHigh) || !fetch.fullSync || fetch.onlyIfNotSynced {
				t.Fatalf("replay app state fetch = %+v, want regular_high full replay", fetch)
			}
			if len(recoveries) != 0 {
				t.Fatalf("async app state recoveries = %v, want none", recoveries)
			}
			if len(muteCalls) != 1 || muteCalls[0].mute != tc.mute {
				t.Fatalf("mute calls = %+v, want mute=%t", muteCalls, tc.mute)
			}
		})
	}
}

func TestArchiveChatPersistsAppStateDeltasFetchedBeforeWrite(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	target := types.JID{User: "456", Server: types.DefaultUserServer}
	pending := types.JID{User: "789", Server: types.DefaultUserServer}
	when := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	for _, chat := range []types.JID{target, pending} {
		if err := a.db.UpsertChat(chat.String(), "dm", "Bob", when); err != nil {
			t.Fatalf("UpsertChat: %v", err)
		}
	}
	f.appStateFetchEvent = func(name string, fullSync, onlyIfNotSynced bool) interface{} {
		return &events.Archive{
			JID:    pending,
			Action: &waSyncAction.ArchiveChatAction{Archived: proto.Bool(true)},
		}
	}

	if err := a.ArchiveChat(context.Background(), target, true); err != nil {
		t.Fatalf("ArchiveChat: %v", err)
	}

	chat, err := a.db.GetChat(pending.String())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if !chat.Archived {
		t.Fatalf("pending app-state archive was not persisted: %+v", chat)
	}
	f.mu.Lock()
	handlers := len(f.handlers)
	f.mu.Unlock()
	if handlers != 0 {
		t.Fatalf("event handlers after chat-state catch-up = %d, want 0", handlers)
	}
}

func TestArchiveChatMarksRecoveryBeforeCursorAdvancingFetch(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	markerSeen := false
	f.appStateFetchEvent = func(name string, fullSync, onlyIfNotSynced bool) interface{} {
		var err error
		markerSeen, err = a.db.AppStateRecoveryRequired(name)
		if err != nil {
			t.Fatalf("AppStateRecoveryRequired during fetch: %v", err)
		}
		return nil
	}

	target := types.JID{User: "456", Server: types.DefaultUserServer}
	if err := a.ArchiveChat(context.Background(), target, true); err != nil {
		t.Fatalf("ArchiveChat: %v", err)
	}
	if !markerSeen {
		t.Fatal("recovery marker was not durable before app-state fetch")
	}
	required, err := a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularLow))
	if err != nil {
		t.Fatalf("AppStateRecoveryRequired after fetch: %v", err)
	}
	if !required {
		t.Fatal("outbound recovery intent was cleared before a later full replay")
	}
}

func TestArchiveChatDoesNotAttributeConcurrentCollectionEvents(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	handlerID := f.AddEventHandler(func(evt interface{}) {
		a.handleAppStatePersistenceEvent(context.Background(), evt, nil)
	})
	defer f.RemoveEventHandler(handlerID)

	target := types.JID{User: "456", Server: types.DefaultUserServer}
	remoteArchive := types.JID{User: "123", Server: types.DefaultUserServer}
	remoteMute := types.JID{User: "789", Server: types.DefaultUserServer}
	when := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	for _, chat := range []types.JID{target, remoteArchive, remoteMute} {
		if err := a.db.UpsertChat(chat.String(), "dm", "Alice", when); err != nil {
			t.Fatalf("UpsertChat: %v", err)
		}
	}
	raw, err := sql.Open("sqlite3", filepath.Join(a.opts.StoreDir, "wacli.db"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`
		CREATE TRIGGER fail_unrelated_mute_persistence
		BEFORE UPDATE OF muted_until ON chats
		BEGIN
			SELECT RAISE(FAIL, 'injected unrelated mute persistence failure');
		END
	`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}
	unrelatedMarkerSeen := false
	f.appStateFetchEvent = func(name string, fullSync, onlyIfNotSynced bool) interface{} {
		f.emit(&events.Mute{
			JID:    remoteMute,
			Action: &waSyncAction.MuteAction{Muted: proto.Bool(true)},
		})
		var err error
		unrelatedMarkerSeen, err = a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularHigh))
		if err != nil {
			t.Fatalf("AppStateRecoveryRequired during queued event: %v", err)
		}
		return &events.Archive{
			JID:    remoteArchive,
			Action: &waSyncAction.ArchiveChatAction{Archived: proto.Bool(true)},
		}
	}

	if err := a.ArchiveChat(context.Background(), target, true); err != nil {
		t.Fatalf("ArchiveChat: %v", err)
	}
	if !unrelatedMarkerSeen {
		t.Fatal("queued live event returned without a durable recovery intent")
	}
	stored, err := a.db.GetChat(remoteArchive.String())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if !stored.Archived {
		t.Fatalf("requested collection event was not persisted: %+v", stored)
	}
	required, err := a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularLow))
	if err != nil {
		t.Fatalf("AppStateRecoveryRequired: %v", err)
	}
	if !required {
		t.Fatal("outbound recovery intent was cleared before a later full replay")
	}
	required, err = a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularHigh))
	if err != nil {
		t.Fatalf("AppStateRecoveryRequired unrelated collection: %v", err)
	}
	if !required {
		t.Fatal("unrelated collection persistence failure was not marked for its own replay")
	}
}

func TestArchiveChatDoesNotClearConcurrentSameCollectionFailure(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	handlerID := f.AddEventHandler(func(evt interface{}) {
		a.handleAppStatePersistenceEvent(context.Background(), evt, nil)
	})
	defer f.RemoveEventHandler(handlerID)

	target := types.JID{User: "456", Server: types.DefaultUserServer}
	remoteArchive := types.JID{User: "123", Server: types.DefaultUserServer}
	when := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	for _, chat := range []types.JID{target, remoteArchive} {
		if err := a.db.UpsertChat(chat.String(), "dm", "Alice", when); err != nil {
			t.Fatalf("UpsertChat: %v", err)
		}
	}
	raw, err := sql.Open("sqlite3", filepath.Join(a.opts.StoreDir, "wacli.db"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`
		CREATE TRIGGER fail_concurrent_archive_persistence
		BEFORE UPDATE OF archived ON chats
		WHEN NEW.jid = '123@s.whatsapp.net'
		BEGIN
			SELECT RAISE(FAIL, 'injected concurrent archive persistence failure');
		END
	`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}
	f.appStateFetchEvent = func(name string, fullSync, onlyIfNotSynced bool) interface{} {
		f.emit(&events.Archive{
			JID:    remoteArchive,
			Action: &waSyncAction.ArchiveChatAction{Archived: proto.Bool(true)},
		})
		return nil
	}

	err = a.ArchiveChat(context.Background(), target, true)
	if err == nil || !strings.Contains(err.Error(), "recovery changed during regular_low synchronization") {
		t.Fatalf("ArchiveChat error = %v, want concurrent recovery change", err)
	}
	f.mu.Lock()
	archiveCalls := len(f.archiveCalls)
	f.mu.Unlock()
	if archiveCalls != 0 {
		t.Fatalf("archive calls = %d, want 0 after queued persistence failure", archiveCalls)
	}
	required, err := a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularLow))
	if err != nil {
		t.Fatalf("AppStateRecoveryRequired: %v", err)
	}
	if !required {
		t.Fatal("concurrent same-collection failure marker was cleared")
	}
}

func TestArchiveChatOrdersFetchedEventsBeforeNewerLiveEvents(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	target := types.JID{User: "456", Server: types.DefaultUserServer}
	remoteArchive := types.JID{User: "123", Server: types.DefaultUserServer}
	when := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	for _, chat := range []types.JID{target, remoteArchive} {
		if err := a.db.UpsertChat(chat.String(), "dm", "Alice", when); err != nil {
			t.Fatalf("UpsertChat: %v", err)
		}
	}
	handlerID := f.AddEventHandler(func(evt interface{}) {
		a.handleAppStatePersistenceEvent(context.Background(), evt, nil)
	})
	defer f.RemoveEventHandler(handlerID)
	f.appStateFetchEvent = func(name string, fullSync, onlyIfNotSynced bool) interface{} {
		f.emit(&events.Archive{
			JID:       remoteArchive,
			Timestamp: when.Add(2 * time.Minute),
			Action:    &waSyncAction.ArchiveChatAction{Archived: proto.Bool(false)},
		})
		return &events.Archive{
			JID:       remoteArchive,
			Timestamp: when.Add(time.Minute),
			Action:    &waSyncAction.ArchiveChatAction{Archived: proto.Bool(true)},
		}
	}

	if err := a.ArchiveChat(context.Background(), target, true); err != nil {
		t.Fatalf("ArchiveChat: %v", err)
	}
	stored, err := a.db.GetChat(remoteArchive.String())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if stored.Archived {
		t.Fatalf("older fetched event overwrote newer live event: %+v", stored)
	}
}

func TestArchiveChatPersistsRecoveredMarkUnread(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	handlerID := f.AddEventHandler(func(evt interface{}) {
		switch v := evt.(type) {
		case *events.MarkChatAsRead:
			a.handleAppStatePersistenceEvent(context.Background(), v, nil)
		case *events.Receipt:
			a.handleReceiptPersistenceEvent(context.Background(), v)
		}
	})
	defer f.RemoveEventHandler(handlerID)

	target := types.JID{User: "456", Server: types.DefaultUserServer}
	remote := types.JID{User: "123", Server: types.DefaultUserServer}
	when := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	for _, chat := range []types.JID{target, remote} {
		if err := a.db.UpsertChat(chat.String(), "dm", "Alice", when); err != nil {
			t.Fatalf("UpsertChat: %v", err)
		}
	}
	f.appStateFetchEvent = func(name string, fullSync, onlyIfNotSynced bool) interface{} {
		return &events.MarkChatAsRead{
			JID:       remote,
			Timestamp: when,
			Action:    &waSyncAction.MarkChatAsReadAction{Read: proto.Bool(false)},
		}
	}

	if err := a.ArchiveChat(context.Background(), target, true); err != nil {
		t.Fatalf("ArchiveChat: %v", err)
	}
	stored, err := a.db.GetChat(remote.String())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if !stored.Unread {
		t.Fatalf("recovered mark-unread was not persisted: %+v", stored)
	}
}

func TestMarkChatReadOrdersPreSendReceiptBeforeLocalMutation(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	handlerID := f.AddEventHandler(func(evt interface{}) {
		if receipt, ok := evt.(*events.Receipt); ok {
			a.handleReceiptPersistenceEvent(context.Background(), receipt)
		}
	})
	defer f.RemoveEventHandler(handlerID)

	target := types.JID{User: "456", Server: types.DefaultUserServer}
	if err := a.db.UpsertChat(target.String(), "dm", "Alice", nowUTC()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	f.markReadBeforeApply = func() {
		f.emit(&events.Receipt{
			MessageSource: types.MessageSource{Chat: target},
			Type:          types.ReceiptTypeReadSelf,
		})
	}

	if err := a.MarkChatRead(context.Background(), target, false); err != nil {
		t.Fatalf("MarkChatRead: %v", err)
	}
	stored, err := a.db.GetChat(target.String())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if !stored.Unread {
		t.Fatalf("pre-send receipt overwrote local mark-unread: %+v", stored)
	}
}

func TestReadReceiptPersistsBeforeClose(t *testing.T) {
	storeDir := t.TempDir()
	a, err := New(Options{StoreDir: storeDir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a.wa = newFakeWA()
	target := types.JID{User: "456", Server: types.DefaultUserServer}
	if err := a.db.UpsertChat(target.String(), "dm", "Alice", nowUTC()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := a.db.SetChatUnreadCount(target.String(), 2); err != nil {
		t.Fatalf("SetChatUnreadCount: %v", err)
	}
	a.handleReceiptPersistenceEvent(context.Background(), &events.Receipt{
		MessageSource: types.MessageSource{Chat: target},
		Type:          types.ReceiptTypeReadSelf,
	})
	a.Close()
	db, err := store.Open(filepath.Join(storeDir, "wacli.db"))
	if err != nil {
		t.Fatalf("Open persisted store: %v", err)
	}
	defer db.Close()
	stored, err := db.GetChat(target.String())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if stored.Unread || stored.UnreadCount != 0 {
		t.Fatalf("receipt was not persisted before close: %+v", stored)
	}
}

func TestReadReceiptResolvesCanonicalJID(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	lid := types.JID{User: "456", Server: types.HiddenUserServer}
	pn := types.JID{User: "123", Server: types.DefaultUserServer}
	if err := a.db.UpsertChat(pn.String(), "dm", "Alice", nowUTC()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := a.db.SetChatUnreadCount(pn.String(), 2); err != nil {
		t.Fatalf("SetChatUnreadCount: %v", err)
	}
	f.mu.Lock()
	f.lids[lid.ToNonAD()] = pn
	f.mu.Unlock()
	a.handleReceiptPersistenceEvent(context.Background(), &events.Receipt{
		MessageSource: types.MessageSource{Chat: lid},
		Type:          types.ReceiptTypeReadSelf,
	})
	chat, err := a.db.GetChat(pn.String())
	if err != nil {
		t.Fatalf("GetChat PN: %v", err)
	}
	if chat.Unread || chat.UnreadCount != 0 {
		t.Fatalf("canonical receipt was not persisted: %+v", chat)
	}
	if _, err := a.db.GetChat(lid.String()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale LID chat was created: %v", err)
	}
}

func TestReadReceiptDoesNotWaitForAppStateQueue(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	target := types.JID{User: "456", Server: types.DefaultUserServer}
	if err := a.db.UpsertChat(target.String(), "dm", "Alice", nowUTC()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := a.db.SetChatUnreadCount(target.String(), 2); err != nil {
		t.Fatalf("SetChatUnreadCount: %v", err)
	}
	blocker := a.appStatePersist.reserve()
	a.handleReceiptPersistenceEvent(context.Background(), &events.Receipt{
		MessageSource: types.MessageSource{Chat: target},
		Type:          types.ReceiptTypeReadSelf,
	})
	stored, err := a.db.GetChat(target.String())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if stored.Unread || stored.UnreadCount != 0 {
		t.Fatalf("receipt waited for unrelated app-state work: %+v", stored)
	}
	a.appStatePersist.completeOne(blocker, func() {})
}

func TestReadReceiptDoesNotClearPendingLocalRecoveryIntent(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	target := types.JID{User: "456", Server: types.DefaultUserServer}
	if err := a.db.UpsertChat(target.String(), "dm", "Alice", nowUTC()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	pending, err := a.beginLocalAppStateWrite(appstate.WAPatchRegularLow)
	if err != nil {
		t.Fatalf("beginLocalAppStateWrite: %v", err)
	}
	a.handleReceiptPersistenceEvent(context.Background(), &events.Receipt{
		MessageSource: types.MessageSource{Chat: target},
		Type:          types.ReceiptTypeReadSelf,
	})
	required, err := a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularLow))
	if err != nil {
		t.Fatalf("AppStateRecoveryRequired: %v", err)
	}
	if !required {
		t.Fatal("receipt cleared the pending local-write recovery intent")
	}
	if err := a.db.ClearAppStateRecoveryIntent(string(pending.collection), pending.generation); err != nil {
		t.Fatalf("ClearAppStateRecoveryIntent: %v", err)
	}
	required, err = a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularLow))
	if err != nil {
		t.Fatalf("AppStateRecoveryRequired after local clear: %v", err)
	}
	if required {
		t.Fatal("receipt recovery intent remained after successful persistence")
	}
}

func TestArchiveChatOrdersPostSendEventsBeforeNewerLiveEventDuringApply(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	handlerID := f.AddEventHandler(func(evt interface{}) {
		a.handleAppStatePersistenceEvent(context.Background(), evt, nil)
	})
	defer f.RemoveEventHandler(handlerID)

	target := types.JID{User: "456", Server: types.DefaultUserServer}
	when := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	if err := a.db.UpsertChat(target.String(), "dm", "Alice", when); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	f.archiveEvent = func() interface{} {
		f.emit(&events.Archive{
			JID:       target,
			Timestamp: nowUTC().Add(time.Minute),
			Action:    &waSyncAction.ArchiveChatAction{Archived: proto.Bool(false)},
		})
		return &events.Archive{
			JID:       target,
			Timestamp: nowUTC(),
			Action:    &waSyncAction.ArchiveChatAction{Archived: proto.Bool(true)},
		}
	}

	if err := a.ArchiveChat(context.Background(), target, true); err != nil {
		t.Fatalf("ArchiveChat: %v", err)
	}
	stored, err := a.db.GetChat(target.String())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if stored.Archived {
		t.Fatalf("local write reserved after newer live event: %+v", stored)
	}
}

func TestArchiveChatReplaysEventDispatchedAfterWriteCompletion(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	handlerID := f.AddEventHandler(func(evt interface{}) {
		a.handleAppStatePersistenceEvent(context.Background(), evt, nil)
	})
	defer f.RemoveEventHandler(handlerID)

	target := types.JID{User: "456", Server: types.DefaultUserServer}
	when := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	if err := a.db.UpsertChat(target.String(), "dm", "Alice", when); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	f.archiveEvent = func() interface{} {
		return &events.Archive{
			JID:       target,
			Timestamp: when.Add(2 * time.Minute),
			Action:    &waSyncAction.ArchiveChatAction{Archived: proto.Bool(true)},
		}
	}

	if err := a.ArchiveChat(context.Background(), target, true); err != nil {
		t.Fatalf("ArchiveChat: %v", err)
	}
	f.emit(&events.Archive{
		JID:       target,
		Timestamp: when.Add(time.Minute),
		Action:    &waSyncAction.ArchiveChatAction{Archived: proto.Bool(false)},
	})
	stored, err := a.db.GetChat(target.String())
	if err != nil {
		t.Fatalf("GetChat after delayed dispatch: %v", err)
	}
	if stored.Archived {
		t.Fatalf("delayed event did not reproduce stale cache: %+v", stored)
	}
	required, err := a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularLow))
	if err != nil {
		t.Fatalf("AppStateRecoveryRequired: %v", err)
	}
	if !required {
		t.Fatal("post-write recovery debt was cleared before delayed dispatch")
	}
	f.appStateFetchEvent = func(name string, fullSync, onlyIfNotSynced bool) interface{} {
		if !fullSync {
			t.Errorf("recovery fetch fullSync = false")
		}
		return &events.Archive{
			JID:       target,
			Timestamp: when.Add(2 * time.Minute),
			Action:    &waSyncAction.ArchiveChatAction{Archived: proto.Bool(true)},
		}
	}
	if err := a.syncChatStateBeforeWrite(context.Background(), appstate.WAPatchRegularLow); err != nil {
		t.Fatalf("syncChatStateBeforeWrite: %v", err)
	}
	stored, err = a.db.GetChat(target.String())
	if err != nil {
		t.Fatalf("GetChat after replay: %v", err)
	}
	if !stored.Archived {
		t.Fatalf("full replay did not recover delayed dispatch: %+v", stored)
	}
	required, err = a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularLow))
	if err != nil {
		t.Fatalf("AppStateRecoveryRequired after replay: %v", err)
	}
	if required {
		t.Fatal("successful replay left recovery debt")
	}
}

func TestConcurrentChatStateWriteCannotReplaySnapshotFromBeforeEarlierWrite(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	first := types.JID{User: "456", Server: types.DefaultUserServer}
	second := types.JID{User: "789", Server: types.DefaultUserServer}
	when := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	for _, chat := range []types.JID{first, second} {
		if err := a.db.UpsertChat(chat.String(), "dm", "Alice", when); err != nil {
			t.Fatalf("UpsertChat: %v", err)
		}
	}

	firstWriteStarted := make(chan struct{})
	releaseFirstWrite := make(chan struct{})
	var writeCount atomic.Int32
	f.archiveEvent = func() interface{} {
		if writeCount.Add(1) == 1 {
			close(firstWriteStarted)
			<-releaseFirstWrite
		}
		return nil
	}

	secondFetchStarted := make(chan struct{})
	var fetchCount atomic.Int32
	f.appStateFetchEvent = func(name string, fullSync, onlyIfNotSynced bool) interface{} {
		if fetchCount.Add(1) != 2 {
			return nil
		}
		stored, err := a.db.GetChat(first.String())
		if err != nil {
			t.Errorf("GetChat during second fetch: %v", err)
			return nil
		}
		close(secondFetchStarted)
		return &events.Archive{
			JID:       first,
			Timestamp: when.Add(time.Minute),
			Action:    &waSyncAction.ArchiveChatAction{Archived: proto.Bool(stored.Archived)},
		}
	}

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- a.ArchiveChat(context.Background(), first, true)
	}()
	<-firstWriteStarted

	secondDone := make(chan error, 1)
	go func() {
		secondDone <- a.ArchiveChat(context.Background(), second, true)
	}()
	select {
	case <-secondFetchStarted:
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseFirstWrite)

	if err := <-firstDone; err != nil {
		t.Fatalf("first ArchiveChat: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second ArchiveChat: %v", err)
	}
	stored, err := a.db.GetChat(first.String())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if !stored.Archived {
		t.Fatalf("stale concurrent replay overwrote earlier local write: %+v", stored)
	}
}

func TestArchiveChatKeepsRecoveryIntentOnAmbiguousSendError(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	f.archiveErr = errors.New("post-send fetch failed")
	a.wa = f

	target := types.JID{User: "456", Server: types.DefaultUserServer}
	if err := a.ArchiveChat(context.Background(), target, true); !errors.Is(err, f.archiveErr) {
		t.Fatalf("ArchiveChat error = %v, want %v", err, f.archiveErr)
	}
	required, err := a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularLow))
	if err != nil {
		t.Fatalf("AppStateRecoveryRequired: %v", err)
	}
	if !required {
		t.Fatal("ambiguous send error cleared outbound recovery intent")
	}
}

func TestArchiveChatPersistsPostSendEventsBeforeClearingIntent(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	target := types.JID{User: "456", Server: types.DefaultUserServer}
	remote := types.JID{User: "789", Server: types.DefaultUserServer}
	when := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	for _, chat := range []types.JID{target, remote} {
		if err := a.db.UpsertChat(chat.String(), "dm", "Alice", when); err != nil {
			t.Fatalf("UpsertChat: %v", err)
		}
	}
	markerSeen := false
	f.archiveEvent = func() interface{} {
		var err error
		markerSeen, err = a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularLow))
		if err != nil {
			t.Fatalf("AppStateRecoveryRequired during send: %v", err)
		}
		return &events.Archive{
			JID:       remote,
			Timestamp: when.Add(time.Minute),
			Action:    &waSyncAction.ArchiveChatAction{Archived: proto.Bool(true)},
		}
	}
	if err := a.ArchiveChat(context.Background(), target, true); err != nil {
		t.Fatalf("ArchiveChat: %v", err)
	}
	if !markerSeen {
		t.Fatal("outbound recovery intent was not durable during send")
	}
	stored, err := a.db.GetChat(remote.String())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if !stored.Archived {
		t.Fatalf("post-send event was not persisted before return: %+v", stored)
	}
	required, err := a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularLow))
	if err != nil {
		t.Fatalf("AppStateRecoveryRequired: %v", err)
	}
	if !required {
		t.Fatal("outbound recovery intent was cleared before a later full replay")
	}
}

func TestLocalAppStateWriteFinishesAfterRequestCancellation(t *testing.T) {
	a := newTestApp(t)
	blockerStarted := make(chan struct{})
	releaseBlocker := make(chan struct{})
	go a.appStatePersist.enqueue(func() {
		close(blockerStarted)
		<-releaseBlocker
	})
	<-blockerStarted

	pending, err := a.beginLocalAppStateWrite(appstate.WAPatchRegularLow)
	if err != nil {
		t.Fatalf("beginLocalAppStateWrite: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	persisted := false
	pending.reserve(a)
	done := make(chan error, 1)
	go func() {
		done <- a.completeLocalAppStateWrite(ctx, &pending, nil, func() error {
			persisted = true
			return nil
		})
	}()
	select {
	case err := <-done:
		t.Fatalf("completeLocalAppStateWrite returned before queued persistence: %v", err)
	default:
	}
	close(releaseBlocker)
	if err := <-done; err != nil {
		t.Fatalf("completeLocalAppStateWrite: %v", err)
	}
	if !persisted {
		t.Fatal("local app state was not persisted after request cancellation")
	}
}

func TestArchiveChatMarkerFailureReplaysReentrantLiveEventInOrder(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	handlerID := f.AddEventHandler(func(evt interface{}) {
		a.handleAppStatePersistenceEvent(context.Background(), evt, nil)
	})
	defer f.RemoveEventHandler(handlerID)

	target := types.JID{User: "456", Server: types.DefaultUserServer}
	remoteArchive := types.JID{User: "123", Server: types.DefaultUserServer}
	when := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	for _, chat := range []types.JID{target, remoteArchive} {
		if err := a.db.UpsertChat(chat.String(), "dm", "Alice", when); err != nil {
			t.Fatalf("UpsertChat: %v", err)
		}
	}
	raw, err := sql.Open("sqlite3", filepath.Join(a.opts.StoreDir, "wacli.db"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer raw.Close()
	f.appStateFetchEvent = func(name string, fullSync, onlyIfNotSynced bool) interface{} {
		if _, err := raw.Exec(`
			CREATE TRIGGER fail_live_recovery_intent
			BEFORE INSERT ON app_state_recovery_intents
			BEGIN
				SELECT RAISE(FAIL, 'injected live recovery intent failure');
			END
		`); err != nil {
			t.Fatalf("create failure trigger: %v", err)
		}
		f.emit(&events.Archive{
			JID:    remoteArchive,
			Action: &waSyncAction.ArchiveChatAction{Archived: proto.Bool(false)},
		})
		if _, err := raw.Exec(`DROP TRIGGER fail_live_recovery_intent`); err != nil {
			t.Fatalf("drop failure trigger: %v", err)
		}
		return &events.Archive{
			JID:    remoteArchive,
			Action: &waSyncAction.ArchiveChatAction{Archived: proto.Bool(true)},
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := a.ArchiveChat(ctx, target, true); err != nil {
		t.Fatalf("ArchiveChat: %v", err)
	}
	stored, err := a.db.GetChat(remoteArchive.String())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if stored.Archived {
		t.Fatalf("older fetched event overwrote unmarked reentrant live event: %+v", stored)
	}
}

func TestMuteChatPersistsOtherAppStateDeltasFetchedBeforeWrite(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	target := types.JID{User: "456", Server: types.DefaultUserServer}
	pending := types.JID{User: "789", Server: types.DefaultUserServer}
	when := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	for _, chat := range []types.JID{target, pending} {
		if err := a.db.UpsertChat(chat.String(), "dm", "Bob", when); err != nil {
			t.Fatalf("UpsertChat: %v", err)
		}
	}
	if err := a.db.UpsertMessage(store.UpsertMessageParams{
		ChatJID:   pending.String(),
		MsgID:     "pending-delete",
		SenderJID: pending.String(),
		Timestamp: when,
		Text:      "delete locally",
	}); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}
	f.appStateFetchEvent = func(name string, fullSync, onlyIfNotSynced bool) interface{} {
		return &events.DeleteForMe{
			ChatJID:   pending,
			MessageID: "pending-delete",
			Timestamp: when.Add(time.Minute),
		}
	}

	if err := a.MuteChat(context.Background(), target, true, 0); err != nil {
		t.Fatalf("MuteChat: %v", err)
	}

	msg, err := a.db.GetMessage(pending.String(), "pending-delete")
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if !msg.DeletedForMe {
		t.Fatalf("pending delete-for-me app state was not persisted: %+v", msg)
	}
}

func TestArchiveChatWaitsForMissingAppStateKey(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "456", Server: types.DefaultUserServer}
	when := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	if err := a.db.UpsertChat(chat.String(), "dm", "Bob", when); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	f.appStateFetchErrs = []error{
		fmt.Errorf("failed to decode regular_low patch: %w", appstate.ErrKeyNotFound),
		nil,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := a.ArchiveChat(ctx, chat, true); err != nil {
		t.Fatalf("ArchiveChat: %v", err)
	}

	f.mu.Lock()
	fetches := append([]fakeAppStateFetch(nil), f.appStateFetches...)
	archiveCalls := len(f.archiveCalls)
	f.mu.Unlock()
	if len(fetches) != 2 {
		t.Fatalf("app state fetches = %d, want 2", len(fetches))
	}
	if fetch := fetches[0]; fetch.name != string(appstate.WAPatchRegularLow) || fetch.fullSync || fetch.onlyIfNotSynced {
		t.Fatalf("initial app state fetch = %+v", fetch)
	}
	if fetch := fetches[1]; fetch.name != string(appstate.WAPatchRegularLow) || !fetch.fullSync || fetch.onlyIfNotSynced {
		t.Fatalf("replay app state fetch = %+v", fetch)
	}
	if archiveCalls != 1 {
		t.Fatalf("archive calls = %d, want 1", archiveCalls)
	}
}

func TestArchiveChatBacksOffWhileWaitingForAppStateKey(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	f.appStateFetchErr = fmt.Errorf("failed to decode regular_low patch: %w", appstate.ErrKeyNotFound)

	ctx, cancel := context.WithTimeout(context.Background(), 1100*time.Millisecond)
	defer cancel()
	err := a.ArchiveChat(ctx, types.JID{User: "456", Server: types.DefaultUserServer}, true)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ArchiveChat error = %v, want context deadline", err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.appStateFetches) > 4 {
		t.Fatalf("app state fetches = %d, want at most 4 including initial delta and replay backoff", len(f.appStateFetches))
	}
	for i, fetch := range f.appStateFetches {
		if fetch.fullSync != (i > 0) {
			t.Fatalf("app state fetch %d = %+v, want initial delta then full replays", i, fetch)
		}
	}
}

func TestArchiveChatWaitsForFullReplayPersistence(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "456", Server: types.DefaultUserServer}
	remoteChat := types.JID{User: "123", Server: types.DefaultUserServer}
	when := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	if err := a.db.UpsertChat(chat.String(), "dm", "Bob", when); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := a.db.UpsertChat(remoteChat.String(), "dm", "Alice", when); err != nil {
		t.Fatalf("UpsertChat remote: %v", err)
	}
	f.appStateFetchErrs = []error{
		fmt.Errorf("failed to verify regular_low patch: %w", appstate.ErrMismatchingLTHash),
		nil,
	}
	replayStarted := make(chan struct{})
	releaseReplay := make(chan struct{})
	f.appStateFetchEvent = func(name string, fullSync, onlyIfNotSynced bool) interface{} {
		if !fullSync {
			return nil
		}
		close(replayStarted)
		<-releaseReplay
		return &events.Archive{
			JID:       remoteChat,
			Timestamp: when.Add(time.Minute),
			Action:    &waSyncAction.ArchiveChatAction{Archived: proto.Bool(true)},
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- a.ArchiveChat(ctx, chat, true)
	}()
	<-replayStarted
	select {
	case err := <-done:
		t.Fatalf("ArchiveChat returned before full replay persisted: %v", err)
	default:
	}
	close(releaseReplay)
	if err := <-done; err != nil {
		t.Fatalf("ArchiveChat: %v", err)
	}

	f.mu.Lock()
	if len(f.appStateFetches) != 2 {
		t.Fatalf("app state fetches = %d, want delta then full replay", len(f.appStateFetches))
	}
	if fetch := f.appStateFetches[0]; fetch.name != string(appstate.WAPatchRegularLow) || fetch.fullSync || fetch.onlyIfNotSynced {
		t.Fatalf("app state fetch = %+v", fetch)
	}
	if fetch := f.appStateFetches[1]; fetch.name != string(appstate.WAPatchRegularLow) || !fetch.fullSync || fetch.onlyIfNotSynced {
		t.Fatalf("replay app state fetch = %+v", fetch)
	}
	if len(f.appStateRecoveries) != 0 {
		t.Fatalf("async app state recoveries = %v, want none", f.appStateRecoveries)
	}
	if len(f.archiveCalls) != 1 {
		t.Fatalf("archive calls = %d, want 1", len(f.archiveCalls))
	}
	f.mu.Unlock()
	stored, err := a.db.GetChat(remoteChat.String())
	if err != nil {
		t.Fatalf("GetChat remote: %v", err)
	}
	if !stored.Archived {
		t.Fatalf("recovery event was not persisted before write returned: %+v", stored)
	}
	required, err := a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularLow))
	if err != nil {
		t.Fatalf("AppStateRecoveryRequired: %v", err)
	}
	if !required {
		t.Fatal("outbound recovery intent was cleared before a later full replay")
	}
}

func TestArchiveChatReplaysAfterRecoveryPersistenceFailure(t *testing.T) {
	storeDir := t.TempDir()
	a, err := New(Options{StoreDir: storeDir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	f := newFakeWA()
	a.wa = f
	target := types.JID{User: "456", Server: types.DefaultUserServer}
	remoteChat := types.JID{User: "123", Server: types.DefaultUserServer}
	when := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	for _, chat := range []types.JID{target, remoteChat} {
		if err := a.db.UpsertChat(chat.String(), "dm", "Alice", when); err != nil {
			t.Fatalf("UpsertChat: %v", err)
		}
	}
	f.appStateFetchErrs = []error{
		fmt.Errorf("failed to verify regular_low patch: %w", appstate.ErrMismatchingLTHash),
		nil,
	}
	f.appStateFetchEvent = func(name string, fullSync, onlyIfNotSynced bool) interface{} {
		if !fullSync {
			return nil
		}
		if err := a.db.Close(); err != nil {
			t.Fatalf("close DB during replay: %v", err)
		}
		return &events.Archive{
			JID:    remoteChat,
			Action: &waSyncAction.ArchiveChatAction{Archived: proto.Bool(true)},
		}
	}

	err = a.ArchiveChat(context.Background(), target, true)
	if err == nil || !strings.Contains(err.Error(), "persist replayed app state regular_low") {
		t.Fatalf("ArchiveChat error = %v, want replay persistence failure", err)
	}
	f.mu.Lock()
	archiveCalls := len(f.archiveCalls)
	f.mu.Unlock()
	if archiveCalls != 0 {
		t.Fatalf("archive calls = %d, want 0 after recovery persistence failure", archiveCalls)
	}
	a.Close()

	reopened, err := New(Options{StoreDir: storeDir})
	if err != nil {
		t.Fatalf("New reopened: %v", err)
	}
	defer reopened.Close()
	replay := newFakeWA()
	replay.appStateFetchEvent = func(name string, fullSync, onlyIfNotSynced bool) interface{} {
		if name != string(appstate.WAPatchRegularLow) || !fullSync || onlyIfNotSynced {
			return nil
		}
		return &events.Archive{
			JID:       remoteChat,
			Timestamp: when.Add(time.Minute),
			Action:    &waSyncAction.ArchiveChatAction{Archived: proto.Bool(true)},
		}
	}
	reopened.wa = replay
	if err := reopened.ArchiveChat(context.Background(), target, true); err != nil {
		t.Fatalf("ArchiveChat replay: %v", err)
	}
	replay.mu.Lock()
	if len(replay.appStateFetches) != 1 {
		t.Fatalf("app state fetches = %+v, want one full replay", replay.appStateFetches)
	}
	fetch := replay.appStateFetches[0]
	replay.mu.Unlock()
	if fetch.name != string(appstate.WAPatchRegularLow) || !fetch.fullSync || fetch.onlyIfNotSynced {
		t.Fatalf("recovery replay fetch = %+v", fetch)
	}
	stored, err := reopened.db.GetChat(remoteChat.String())
	if err != nil {
		t.Fatalf("GetChat remote: %v", err)
	}
	if !stored.Archived {
		t.Fatalf("replayed recovery was not persisted: %+v", stored)
	}
	required, err := reopened.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularLow))
	if err != nil {
		t.Fatalf("AppStateRecoveryRequired: %v", err)
	}
	if !required {
		t.Fatal("new outbound recovery intent was cleared before a later full replay")
	}
}

func TestArchiveChatMarksReplayAfterDeltaPersistenceFailure(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	target := types.JID{User: "456", Server: types.DefaultUserServer}
	remoteChat := types.JID{User: "123", Server: types.DefaultUserServer}
	when := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	for _, chat := range []types.JID{target, remoteChat} {
		if err := a.db.UpsertChat(chat.String(), "dm", "Alice", when); err != nil {
			t.Fatalf("UpsertChat: %v", err)
		}
	}
	raw, err := sql.Open("sqlite3", filepath.Join(a.opts.StoreDir, "wacli.db"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`
		CREATE TRIGGER fail_archive_persistence
		BEFORE UPDATE OF archived ON chats
		BEGIN
			SELECT RAISE(FAIL, 'injected archive persistence failure');
		END
	`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}
	f.appStateFetchEvent = func(name string, fullSync, onlyIfNotSynced bool) interface{} {
		return &events.Archive{
			JID:       remoteChat,
			Timestamp: when.Add(time.Minute),
			Action:    &waSyncAction.ArchiveChatAction{Archived: proto.Bool(true)},
		}
	}

	err = a.ArchiveChat(context.Background(), target, true)
	if err == nil || !strings.Contains(err.Error(), "persist fetched app state regular_low") {
		t.Fatalf("ArchiveChat error = %v, want delta persistence failure", err)
	}
	required, err := a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularLow))
	if err != nil {
		t.Fatalf("AppStateRecoveryRequired: %v", err)
	}
	if !required {
		t.Fatal("delta persistence failure did not leave a recovery marker")
	}
	if _, err := raw.Exec(`DROP TRIGGER fail_archive_persistence`); err != nil {
		t.Fatalf("drop failure trigger: %v", err)
	}
	if err := a.ArchiveChat(context.Background(), target, true); err != nil {
		t.Fatalf("ArchiveChat replay: %v", err)
	}
	f.mu.Lock()
	fetches := append([]fakeAppStateFetch(nil), f.appStateFetches...)
	archiveCalls := len(f.archiveCalls)
	f.mu.Unlock()
	if len(fetches) != 2 || fetches[0].fullSync || !fetches[1].fullSync {
		t.Fatalf("app state fetches = %+v, want failed delta then full replay", fetches)
	}
	if archiveCalls != 1 {
		t.Fatalf("archive calls = %d, want 1 after successful replay", archiveCalls)
	}
	stored, err := a.db.GetChat(remoteChat.String())
	if err != nil {
		t.Fatalf("GetChat remote: %v", err)
	}
	if !stored.Archived {
		t.Fatalf("full replay was not persisted: %+v", stored)
	}
}

func TestArchiveChatUsesSynchronousFullReplayForMismatch(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	var recoveries sync.Map
	handlerID := f.AddEventHandler(func(evt interface{}) {
		if syncErr, ok := evt.(*events.AppStateSyncError); ok {
			a.handleAppStateSyncError(context.Background(), syncErr, &recoveries)
		}
	})
	defer f.RemoveEventHandler(handlerID)
	f.appStateFetchErrs = []error{
		fmt.Errorf("failed to verify regular_low patch: %w", appstate.ErrMismatchingLTHash),
		nil,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := a.ArchiveChat(ctx, types.JID{User: "456", Server: types.DefaultUserServer}, true); err != nil {
		t.Fatalf("ArchiveChat: %v", err)
	}
	f.mu.Lock()
	fetches := append([]fakeAppStateFetch(nil), f.appStateFetches...)
	archiveCalls := len(f.archiveCalls)
	legacyRecoveries := append([]string(nil), f.appStateRecoveries...)
	f.mu.Unlock()
	if len(fetches) != 2 {
		t.Fatalf("app state fetches = %+v, want delta then synchronous full replay", fetches)
	}
	if fetch := fetches[1]; fetch.name != string(appstate.WAPatchRegularLow) || !fetch.fullSync || fetch.onlyIfNotSynced {
		t.Fatalf("recovery fetch = %+v, want regular_low full replay", fetch)
	}
	if archiveCalls != 1 {
		t.Fatalf("archive calls = %d, want 1 after full replay", archiveCalls)
	}
	if len(legacyRecoveries) != 0 {
		t.Fatalf("legacy async recoveries = %v, want synchronous replay only", legacyRecoveries)
	}
	required, err := a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularLow))
	if err != nil {
		t.Fatalf("AppStateRecoveryRequired: %v", err)
	}
	if !required {
		t.Fatal("outbound recovery intent was cleared before a later full replay")
	}
}

func TestArchiveChatWaitsForPrimaryRecoveryAfterFullReplayMismatch(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	target := types.JID{User: "456", Server: types.DefaultUserServer}
	remote := types.JID{User: "123", Server: types.DefaultUserServer}
	when := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	for _, chat := range []types.JID{target, remote} {
		if err := a.db.UpsertChat(chat.String(), "dm", "Alice", when); err != nil {
			t.Fatalf("UpsertChat: %v", err)
		}
	}
	mismatch := fmt.Errorf("failed to verify regular_low patch: %w", appstate.ErrMismatchingLTHash)
	f.appStateFetchErrs = []error{mismatch, mismatch}
	recoveryRequested := make(chan string, 1)
	releaseRecovery := make(chan struct{})
	f.onAppStateRecovery = func(name string) {
		recoveryRequested <- name
		go func() {
			<-releaseRecovery
			f.emit(&events.Archive{
				JID:       remote,
				Timestamp: when.Add(time.Minute),
				Action:    &waSyncAction.ArchiveChatAction{Archived: proto.Bool(true)},
			})
			f.emit(&events.AppStateSyncComplete{
				Name:     appstate.WAPatchRegularLow,
				Version:  12,
				Recovery: true,
			})
		}()
	}

	done := make(chan error, 1)
	go func() {
		done <- a.ArchiveChat(context.Background(), target, true)
	}()
	select {
	case name := <-recoveryRequested:
		if name != string(appstate.WAPatchRegularLow) {
			t.Fatalf("recovery collection = %q, want regular_low", name)
		}
	case <-time.After(time.Second):
		t.Fatal("primary-device recovery was not requested")
	}
	f.mu.Lock()
	archiveCallsBeforeRecovery := len(f.archiveCalls)
	f.mu.Unlock()
	if archiveCallsBeforeRecovery != 0 {
		t.Fatalf("archive calls before recovery completion = %d, want 0", archiveCallsBeforeRecovery)
	}
	close(releaseRecovery)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ArchiveChat: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ArchiveChat did not continue after recovery completion")
	}

	f.mu.Lock()
	fetches := append([]fakeAppStateFetch(nil), f.appStateFetches...)
	recoveries := append([]string(nil), f.appStateRecoveries...)
	archiveCalls := len(f.archiveCalls)
	handlers := len(f.handlers)
	f.mu.Unlock()
	if len(fetches) != 2 || fetches[0].fullSync || !fetches[1].fullSync {
		t.Fatalf("app state fetches = %+v, want delta then failed full replay", fetches)
	}
	if len(recoveries) != 1 || recoveries[0] != string(appstate.WAPatchRegularLow) {
		t.Fatalf("app state recoveries = %v, want [regular_low]", recoveries)
	}
	if archiveCalls != 1 {
		t.Fatalf("archive calls = %d, want 1 after recovery persistence", archiveCalls)
	}
	if handlers != 0 {
		t.Fatalf("event handlers after primary recovery = %d, want 0", handlers)
	}
	stored, err := a.db.GetChat(remote.String())
	if err != nil {
		t.Fatalf("GetChat remote: %v", err)
	}
	if !stored.Archived {
		t.Fatalf("primary-device recovery event was not persisted: %+v", stored)
	}
	required, err := a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularLow))
	if err != nil {
		t.Fatalf("AppStateRecoveryRequired: %v", err)
	}
	if !required {
		t.Fatal("outbound recovery intent was cleared before a later full replay")
	}
}

func TestArchiveChatKeepsRecoveryIntentWhenPrimaryRecoveryRequestFails(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	mismatch := fmt.Errorf("failed to verify regular_low patch: %w", appstate.ErrMismatchingLTHash)
	f.appStateFetchErrs = []error{mismatch, mismatch}
	f.appStateRecoveryErr = errors.New("injected recovery request failure")

	err := a.ArchiveChat(context.Background(), types.JID{User: "456", Server: types.DefaultUserServer}, true)
	if !errors.Is(err, f.appStateRecoveryErr) {
		t.Fatalf("ArchiveChat error = %v, want %v", err, f.appStateRecoveryErr)
	}
	f.mu.Lock()
	archiveCalls := len(f.archiveCalls)
	handlers := len(f.handlers)
	f.mu.Unlock()
	if archiveCalls != 0 {
		t.Fatalf("archive calls = %d, want 0 after recovery request failure", archiveCalls)
	}
	if handlers != 0 {
		t.Fatalf("event handlers after recovery request failure = %d, want 0", handlers)
	}
	required, markerErr := a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularLow))
	if markerErr != nil {
		t.Fatalf("AppStateRecoveryRequired: %v", markerErr)
	}
	if !required {
		t.Fatal("recovery request failure cleared the durable recovery intent")
	}
}

func TestArchiveChatBoundsPrimaryRecoveryWait(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	mismatch := fmt.Errorf("failed to verify regular_low patch: %w", appstate.ErrMismatchingLTHash)
	f.appStateFetchErrs = []error{mismatch, mismatch}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := a.ArchiveChat(ctx, types.JID{User: "456", Server: types.DefaultUserServer}, true)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ArchiveChat error = %v, want context deadline", err)
	}
	f.mu.Lock()
	recoveries := append([]string(nil), f.appStateRecoveries...)
	archiveCalls := len(f.archiveCalls)
	handlers := len(f.handlers)
	f.mu.Unlock()
	if len(recoveries) != 1 || recoveries[0] != string(appstate.WAPatchRegularLow) {
		t.Fatalf("app state recoveries = %v, want one bounded regular_low request", recoveries)
	}
	if archiveCalls != 0 {
		t.Fatalf("archive calls = %d, want 0 after recovery timeout", archiveCalls)
	}
	if handlers != 0 {
		t.Fatalf("event handlers after recovery timeout = %d, want 0", handlers)
	}
	required, markerErr := a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularLow))
	if markerErr != nil {
		t.Fatalf("AppStateRecoveryRequired: %v", markerErr)
	}
	if !required {
		t.Fatal("recovery timeout cleared the durable recovery intent")
	}
}

func TestArchiveChatKeepsRecoveryIntentWhenPrimaryRecoveryPersistenceFails(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	target := types.JID{User: "456", Server: types.DefaultUserServer}
	remote := types.JID{User: "123", Server: types.DefaultUserServer}
	when := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	for _, chat := range []types.JID{target, remote} {
		if err := a.db.UpsertChat(chat.String(), "dm", "Alice", when); err != nil {
			t.Fatalf("UpsertChat: %v", err)
		}
	}
	raw, err := sql.Open("sqlite3", filepath.Join(a.opts.StoreDir, "wacli.db"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`
		CREATE TRIGGER fail_primary_recovery_persistence
		BEFORE UPDATE OF archived ON chats
		BEGIN
			SELECT RAISE(FAIL, 'injected primary recovery persistence failure');
		END
	`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}
	mismatch := fmt.Errorf("failed to verify regular_low patch: %w", appstate.ErrMismatchingLTHash)
	f.appStateFetchErrs = []error{mismatch, mismatch}
	f.onAppStateRecovery = func(name string) {
		f.emit(&events.Archive{
			JID:       remote,
			Timestamp: when.Add(time.Minute),
			Action:    &waSyncAction.ArchiveChatAction{Archived: proto.Bool(true)},
		})
		f.emit(&events.AppStateSyncComplete{
			Name:     appstate.WAPatchRegularLow,
			Version:  12,
			Recovery: true,
		})
	}

	err = a.ArchiveChat(context.Background(), target, true)
	if err == nil || !strings.Contains(err.Error(), "persist recovered app state regular_low") {
		t.Fatalf("ArchiveChat error = %v, want recovery persistence failure", err)
	}
	f.mu.Lock()
	archiveCalls := len(f.archiveCalls)
	f.mu.Unlock()
	if archiveCalls != 0 {
		t.Fatalf("archive calls = %d, want 0 after recovery persistence failure", archiveCalls)
	}
	required, markerErr := a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularLow))
	if markerErr != nil {
		t.Fatalf("AppStateRecoveryRequired: %v", markerErr)
	}
	if !required {
		t.Fatal("recovery persistence failure cleared the durable recovery intent")
	}
}

func TestArchiveChatWaitsForMissingKeyDuringFullReplay(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	f.appStateFetchErrs = []error{
		fmt.Errorf("failed to verify regular_low patch: %w", appstate.ErrMismatchingLTHash),
		fmt.Errorf("failed to decode regular_low snapshot: %w", appstate.ErrKeyNotFound),
		nil,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := a.ArchiveChat(ctx, types.JID{User: "456", Server: types.DefaultUserServer}, true); err != nil {
		t.Fatalf("ArchiveChat: %v", err)
	}
	f.mu.Lock()
	fetches := append([]fakeAppStateFetch(nil), f.appStateFetches...)
	f.mu.Unlock()
	if len(fetches) != 3 {
		t.Fatalf("app state fetches = %+v, want delta then two full replays", fetches)
	}
	for i, fetch := range fetches[1:] {
		if fetch.name != string(appstate.WAPatchRegularLow) || !fetch.fullSync || fetch.onlyIfNotSynced {
			t.Fatalf("full replay fetch %d = %+v", i+1, fetch)
		}
	}
}

func TestChatStateSerializationRespectsContextCancellation(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	f.appStateFetchErrs = []error{
		fmt.Errorf("failed to verify regular_low patch: %w", appstate.ErrMismatchingLTHash),
		nil,
	}
	replayStarted := make(chan struct{})
	releaseReplay := make(chan struct{})
	f.appStateFetchEvent = func(name string, fullSync, onlyIfNotSynced bool) interface{} {
		if fullSync {
			close(replayStarted)
			<-releaseReplay
		}
		return nil
	}

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- a.ArchiveChat(context.Background(), types.JID{User: "123", Server: types.DefaultUserServer}, true)
	}()
	<-replayStarted
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := a.ArchiveChat(ctx, types.JID{User: "456", Server: types.DefaultUserServer}, true)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second ArchiveChat error = %v, want context deadline", err)
	}
	close(releaseReplay)
	if err := <-firstDone; err != nil {
		t.Fatalf("first ArchiveChat: %v", err)
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

func TestHistorySyncEditedMessageSurvivesOlderOriginal(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "123", Server: types.DefaultUserServer}
	f.contacts[chat.ToNonAD()] = types.ContactInfo{Found: true, FullName: "Alice"}
	base := time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC)
	editMsg := &waWeb.WebMessageInfo{
		Key: &waCommon.MessageKey{
			RemoteJID: proto.String(chat.String()),
			FromMe:    proto.Bool(false),
			ID:        proto.String("edit-event"),
		},
		MessageTimestamp: proto.Uint64(uint64(base.Add(time.Minute).Unix())),
		Message: &waProto.Message{
			ProtocolMessage: &waProto.ProtocolMessage{
				Type: waProto.ProtocolMessage_MESSAGE_EDIT.Enum(),
				Key: &waCommon.MessageKey{
					RemoteJID: proto.String(chat.String()),
					FromMe:    proto.Bool(false),
					ID:        proto.String("original-id"),
				},
				EditedMessage: &waProto.Message{Conversation: proto.String("edited body")},
			},
		},
	}
	originalMsg := &waWeb.WebMessageInfo{
		Key: &waCommon.MessageKey{
			RemoteJID: proto.String(chat.String()),
			FromMe:    proto.Bool(false),
			ID:        proto.String("original-id"),
		},
		MessageTimestamp: proto.Uint64(uint64(base.Unix())),
		Message:          &waProto.Message{Conversation: proto.String("original body")},
	}
	history := &events.HistorySync{
		Data: &waHistorySync.HistorySync{
			SyncType: waHistorySync.HistorySync_FULL.Enum(),
			Conversations: []*waHistorySync.Conversation{{
				ID:       proto.String(chat.String()),
				Messages: []*waHistorySync.HistorySyncMsg{{Message: editMsg}, {Message: originalMsg}},
			}},
		},
	}

	var messagesStored atomic.Int64
	var lastEvent atomic.Int64
	a.handleHistorySync(context.Background(), SyncOptions{}, history, &messagesStored, &lastEvent, func(string, string) {})

	if messagesStored.Load() != 2 {
		t.Fatalf("expected 2 stored attempts, got %d", messagesStored.Load())
	}
	if n, err := a.db.CountMessages(); err != nil || n != 1 {
		t.Fatalf("expected 1 stored row, got %d (err=%v)", n, err)
	}
	msg, err := a.db.GetMessage(chat.String(), "original-id")
	if err != nil {
		t.Fatalf("GetMessage edited original: %v", err)
	}
	if msg.Text != "edited body" || msg.DisplayText != "edited body" {
		t.Fatalf("older original clobbered edit: %+v", msg)
	}
	if !msg.Timestamp.Equal(base) {
		t.Fatalf("timestamp = %s, want original timestamp", msg.Timestamp)
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

func TestSyncDownloadsHistoryNotificationBeforeProcessing(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "555", Server: types.DefaultUserServer}
	base := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	syncType := waE2E.HistorySyncType_INITIAL_BOOTSTRAP
	notif := &waE2E.HistorySyncNotification{SyncType: &syncType}
	f.connectEvents = []interface{}{&events.Message{
		Message: &waProto.Message{
			ProtocolMessage: &waProto.ProtocolMessage{
				HistorySyncNotification: notif,
			},
		},
	}}
	downloadCalls := 0
	f.downloadHistory = func(got *waE2E.HistorySyncNotification) (*waHistorySync.HistorySync, error) {
		downloadCalls++
		if got != notif {
			t.Fatalf("DownloadHistorySync notification = %p, want %p", got, notif)
		}
		return historySyncWithTextMessages(chat, base, "m-hist").Data, nil
	}

	res, err := a.Sync(context.Background(), SyncOptions{
		Mode:     SyncModeOnce,
		AllowQR:  false,
		IdleExit: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if downloadCalls != 1 {
		t.Fatalf("download calls = %d, want 1", downloadCalls)
	}
	if res.MessagesStored != 1 {
		t.Fatalf("messages stored = %d, want 1", res.MessagesStored)
	}
	if got := f.deleteHistoryCalls; len(got) != 1 || got[0] != notif {
		t.Fatalf("delete history calls = %v, want notification %p", got, notif)
	}
	if got := f.manualHistorySyncCalls; len(got) != 2 || !got[0] || got[1] {
		t.Fatalf("manual history calls = %v", got)
	}
}

func TestStoreParsedStatusMessageUsesSeparateStatusTable(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	err := a.storeParsedMessage(context.Background(), wa.ParsedMessage{
		Chat:      types.StatusBroadcastJID,
		ID:        "status-incoming",
		SenderJID: "15551234567@s.whatsapp.net",
		Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		Text:      "incoming status",
	})
	if err != nil {
		t.Fatalf("storeParsedMessage: %v", err)
	}
	if _, err := a.db.GetMessage(types.StatusBroadcastJID.String(), "status-incoming"); err == nil {
		t.Fatalf("status retrieval was stored as a regular message")
	}
	status, err := a.db.GetStatusMessage("status-incoming")
	if err != nil {
		t.Fatalf("GetStatusMessage: %v", err)
	}
	if status.MsgID != "status-incoming" || status.Text != "incoming status" || status.SenderJID != "15551234567@s.whatsapp.net" {
		t.Fatalf("unexpected status retrieval: %+v", status)
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
					StanzaID:    proto.String("m-text"),
					Participant: proto.String("123@s.whatsapp.net"),
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
	if msg.QuotedMsgID != "m-text" || msg.QuotedSenderJID != "123@s.whatsapp.net" {
		t.Fatalf("unexpected quoted metadata: id=%q sender=%q", msg.QuotedMsgID, msg.QuotedSenderJID)
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

func TestSyncDownloadMediaCanonicalizesLIDChatBeforeEnqueue(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	lid := types.JID{User: "152527844733129", Server: types.HiddenUserServer}
	pn := types.JID{User: "447356168511", Server: types.DefaultUserServer}
	f.lids[lid] = pn
	f.contacts[pn] = types.ContactInfo{Found: true, FullName: "Dave", PushName: "Dave"}

	msgID := "media-lid"
	f.connectEvents = append(f.connectEvents, &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:     lid,
				Sender:   lid,
				IsFromMe: false,
			},
			ID:        msgID,
			Timestamp: time.Date(2026, 5, 15, 17, 30, 0, 0, time.UTC),
			PushName:  "Dave",
		},
		Message: &waProto.Message{
			ImageMessage: &waProto.ImageMessage{
				Mimetype:      proto.String("image/jpeg"),
				DirectPath:    proto.String("/direct"),
				MediaKey:      []byte{1, 2, 3},
				FileSHA256:    []byte{4, 5, 6},
				FileEncSHA256: []byte{7, 8, 9},
				FileLength:    proto.Uint64(4),
			},
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := a.Sync(ctx, SyncOptions{
		Mode:          SyncModeOnce,
		AllowQR:       false,
		DownloadMedia: true,
		IdleExit:      100 * time.Millisecond,
	}); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	msg, err := a.db.GetMessage(pn.String(), msgID)
	if err != nil {
		t.Fatalf("GetMessage canonical PN row: %v", err)
	}
	if msg.LocalPath == "" {
		t.Fatalf("expected media downloaded via canonical PN row, got empty local_path")
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

// TestSyncOnceDrainsMediaBeforeExit guards the fix where once-mode idle-exit
// used to cancel media downloads still queued or in flight. Each download takes
// far longer than IdleExit, so without the graceful drain the sync would exit
// and cancel most of them, leaving local_path empty.
func TestSyncOnceDrainsMediaBeforeExit(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	// Download work (12 jobs / 4 workers * 150ms ≈ 450ms) exceeds the 250ms idle
	// poll inside runSyncUntilIdle, making premature worker cancellation deterministic.
	f.downloadDelay = 150 * time.Millisecond

	chat := types.JID{User: "123", Server: types.DefaultUserServer}
	f.contacts[chat.ToNonAD()] = types.ContactInfo{Found: true, FullName: "Alice", PushName: "Alice"}

	const n = 12
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("media-%02d", i)
		ids = append(ids, id)
		f.connectEvents = append(f.connectEvents, &events.Message{
			Info: types.MessageInfo{
				MessageSource: types.MessageSource{Chat: chat, Sender: chat, IsFromMe: false},
				ID:            id,
				Timestamp:     base.Add(time.Duration(i) * time.Second),
				PushName:      "Alice",
			},
			Message: &waProto.Message{
				ImageMessage: &waProto.ImageMessage{
					Mimetype:      proto.String("image/jpeg"),
					DirectPath:    proto.String("/direct"),
					MediaKey:      []byte{1, 2, 3},
					FileSHA256:    []byte{4, 5, 6},
					FileEncSHA256: []byte{7, 8, 9},
					FileLength:    proto.Uint64(4),
				},
			},
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := a.Sync(ctx, SyncOptions{
		Mode:          SyncModeOnce,
		AllowQR:       false,
		DownloadMedia: true,
		IdleExit:      10 * time.Millisecond,
	}); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	for _, id := range ids {
		msg, err := a.db.GetMessage(chat.String(), id)
		if err != nil {
			t.Fatalf("GetMessage %s: %v", id, err)
		}
		if msg.LocalPath == "" {
			t.Fatalf("expected media %s downloaded before exit, got empty local_path", id)
		}
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

func TestSyncFollowReconnectsAfterStreamReplaced(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	reconnected := make(chan struct{})
	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				f.mu.Lock()
				connectCalls := f.connectCalls
				f.mu.Unlock()
				if connectCalls >= 2 {
					close(reconnected)
					cancel()
					return
				}
			}
		}
	}()

	_, err := a.Sync(ctx, SyncOptions{
		Mode:         SyncModeFollow,
		AllowQR:      false,
		MaxReconnect: time.Second,
		AfterConnect: func(context.Context) error {
			f.emit(&events.StreamReplaced{})
			return nil
		},
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Sync: %v", err)
	}

	select {
	case <-reconnected:
	default:
		f.mu.Lock()
		connectCalls := f.connectCalls
		f.mu.Unlock()
		t.Fatalf("expected StreamReplaced to trigger reconnect, connect calls = %d", connectCalls)
	}
}

func TestSyncFollowReconnectsWhenStaleThresholdExceeded(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	reconnected := make(chan struct{})
	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				f.mu.Lock()
				connectCalls := f.connectCalls
				f.mu.Unlock()
				if connectCalls >= 2 {
					close(reconnected)
					cancel()
					return
				}
			}
		}
	}()

	_, err := a.Sync(ctx, SyncOptions{
		Mode:           SyncModeFollow,
		AllowQR:        false,
		MaxReconnect:   time.Second,
		StaleThreshold: 200 * time.Millisecond,
		AfterConnect: func(context.Context) error {
			time.Sleep(250 * time.Millisecond)
			f.emit(&events.KeepAliveTimeout{ErrorCount: 2, LastSuccess: nowUTC().Add(-250 * time.Millisecond)})
			return nil
		},
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Sync: %v", err)
	}

	select {
	case <-reconnected:
	default:
		f.mu.Lock()
		connectCalls := f.connectCalls
		f.mu.Unlock()
		t.Fatalf("expected stale threshold to trigger reconnect, connect calls = %d", connectCalls)
	}
}

func TestSyncFollowStaleThresholdDisablesAutoReconnectWhileConnected(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	var sawDisabled bool
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := a.Sync(ctx, SyncOptions{
		Mode:           SyncModeFollow,
		AllowQR:        false,
		StaleThreshold: time.Second,
		AfterConnect: func(context.Context) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.autoReconnect {
				return fmt.Errorf("auto reconnect still enabled during stale-threshold sync")
			}
			sawDisabled = true
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if !sawDisabled {
		t.Fatal("AfterConnect did not observe disabled auto reconnect")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.autoReconnect {
		t.Fatal("auto reconnect was restored while fake client remained connected")
	}
}

func TestSyncFollowIgnoresKeepAliveTimeoutFromPreviousConnection(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	f.connected = true

	var messagesStored atomic.Int64
	var connectionEpoch atomic.Int64
	connectionEpoch.Store(nowUTC().UnixNano())
	disconnected := make(chan struct{}, 1)
	staleReconnect := make(chan staleReconnectRequest, 1)
	staleReconnect <- staleReconnectRequest{
		threshold:   200 * time.Millisecond,
		idle:        time.Minute,
		errorCount:  2,
		lastSuccess: time.Unix(0, connectionEpoch.Load()).Add(-time.Minute),
		source:      "keepalive_timeout",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := a.runSyncFollow(ctx, time.Second, SyncPresenceModeNormal, &messagesStored, &connectionEpoch, disconnected, staleReconnect)
	if err != nil {
		t.Fatalf("runSyncFollow: %v", err)
	}

	if !f.IsConnected() {
		t.Fatal("previous-connection keepalive timeout closed current connection")
	}
	f.mu.Lock()
	connectCalls := f.connectCalls
	f.mu.Unlock()
	if connectCalls != 0 {
		t.Fatalf("previous-connection keepalive timeout reconnected, connect calls = %d", connectCalls)
	}
}

func TestSyncFollowDoesNotReconnectWhenKeepAliveFailureIsRecent(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				f.emit(&events.KeepAliveTimeout{ErrorCount: 1, LastSuccess: nowUTC()})
			}
		}
	}()

	time.AfterFunc(600*time.Millisecond, func() {
		f.mu.Lock()
		connectCalls := f.connectCalls
		f.mu.Unlock()
		if connectCalls > 1 {
			cancel()
			t.Errorf("unexpected reconnect with recent keepalive failure, connect calls = %d", connectCalls)
			return
		}
		cancel()
	})

	_, err := a.Sync(ctx, SyncOptions{
		Mode:           SyncModeFollow,
		AllowQR:        false,
		StaleThreshold: time.Minute,
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Sync: %v", err)
	}
}

func TestSyncRejectsIneffectiveStaleThreshold(t *testing.T) {
	a := newTestApp(t)

	_, err := a.Sync(context.Background(), SyncOptions{
		Mode:           SyncModeFollow,
		StaleThreshold: MaxStaleThreshold(),
	})
	if err == nil || !strings.Contains(err.Error(), "upstream auto-reconnect threshold") {
		t.Fatalf("expected stale threshold validation error, got %v", err)
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

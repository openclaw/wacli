package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
	if msg.Text != "" || msg.DisplayText != store.DeletedForMeMessageDisplayText {
		t.Fatalf("text=%q display=%q", msg.Text, msg.DisplayText)
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

func TestArchiveChatUsesLatestMessageRange(t *testing.T) {
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

	if err := a.ArchiveChat(context.Background(), chat, true); err != nil {
		t.Fatalf("ArchiveChat: %v", err)
	}

	f.mu.Lock()
	calls := append([]fakeArchiveCall(nil), f.archiveCalls...)
	f.mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("archive calls = %d", len(calls))
	}
	if !calls[0].lastMsgTS.Equal(when) {
		t.Fatalf("lastMsgTS = %s, want %s", calls[0].lastMsgTS, when)
	}
	if calls[0].lastMsgKey == nil || calls[0].lastMsgKey.GetID() != "latest" || !calls[0].lastMsgKey.GetFromMe() {
		t.Fatalf("lastMsgKey = %+v", calls[0].lastMsgKey)
	}
	c, err := a.db.GetChat(chat.String())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if !c.Archived {
		t.Fatalf("expected local archived state, got %+v", c)
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

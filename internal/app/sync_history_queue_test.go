package app

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func historyNotificationEvent(id string, syncType waE2E.HistorySyncType) (*events.Message, *waE2E.HistorySyncNotification) {
	notif := &waE2E.HistorySyncNotification{SyncType: syncType.Enum(), FileLength: proto.Uint64(42)}
	return &events.Message{
		Info: types.MessageInfo{ID: types.MessageID(id)},
		Message: &waE2E.Message{
			ProtocolMessage: &waE2E.ProtocolMessage{HistorySyncNotification: notif},
		},
	}, notif
}

func liveTextEvent(chat types.JID, id string, ts time.Time) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat},
			ID:            types.MessageID(id),
			Timestamp:     ts,
			PushName:      "Alice",
		},
		Message: &waE2E.Message{Conversation: proto.String("hello")},
	}
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSyncStoresQueuedHistoryWithoutHoldingLiveMessages(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "555", Server: types.DefaultUserServer}
	base := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	histEvt, notif := historyNotificationEvent("hist-1", waE2E.HistorySyncType_FULL)
	f.connectEvents = []any{histEvt, liveTextEvent(chat, "m-live", base.Add(time.Hour))}

	var liveFirst atomic.Bool
	f.downloadHistory = func(got *waE2E.HistorySyncNotification) (*waHistorySync.HistorySync, error) {
		if !proto.Equal(got, notif) {
			t.Errorf("DownloadHistorySync notification = %v, want %v", got, notif)
		}
		// The chunk is downloaded by the worker after the handler moved on:
		// the live message that followed the notification is already stored.
		if _, err := a.db.GetMessage(chat.String(), "m-live"); err == nil {
			liveFirst.Store(true)
		}
		return historySyncWithTextMessages(chat, base, "m-hist").Data, nil
	}

	res, err := a.Sync(context.Background(), SyncOptions{Mode: SyncModeOnce, IdleExit: 10 * time.Millisecond})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if !liveFirst.Load() {
		t.Fatalf("the live message waited for the history chunk")
	}
	for _, id := range []string{"m-live", "m-hist"} {
		if _, err := a.db.GetMessage(chat.String(), id); err != nil {
			t.Fatalf("GetMessage %s: %v", id, err)
		}
	}
	if res.MessagesStored != 2 {
		t.Fatalf("messages stored = %d, want 2", res.MessagesStored)
	}
	if n, err := a.db.CountHistorySyncQueue(); err != nil || n != 0 {
		t.Fatalf("queue = %d (err %v), want empty", n, err)
	}
	if len(f.deleteHistoryCalls) != 1 {
		t.Fatalf("delete history calls = %d, want 1", len(f.deleteHistoryCalls))
	}
}

func TestSyncStoresHistoryLeftQueuedByAnEarlierRun(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "555", Server: types.DefaultUserServer}
	base := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	_, notif := historyNotificationEvent("hist-left", waE2E.HistorySyncType_FULL)
	raw, err := proto.Marshal(notif)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := a.db.EnqueueHistorySync("hist-left", int32(notif.GetSyncType()), raw, base); err != nil {
		t.Fatalf("EnqueueHistorySync: %v", err)
	}
	f.downloadHistory = func(got *waE2E.HistorySyncNotification) (*waHistorySync.HistorySync, error) {
		return historySyncWithTextMessages(chat, base, "m-left").Data, nil
	}

	if _, err := a.Sync(context.Background(), SyncOptions{Mode: SyncModeOnce, IdleExit: 10 * time.Millisecond}); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if _, err := a.db.GetMessage(chat.String(), "m-left"); err != nil {
		t.Fatalf("GetMessage m-left: %v", err)
	}
	if n, err := a.db.CountHistorySyncQueue(); err != nil || n != 0 {
		t.Fatalf("queue = %d (err %v), want empty", n, err)
	}
	if len(f.deleteHistoryCalls) != 1 {
		t.Fatalf("delete history calls = %d, want 1", len(f.deleteHistoryCalls))
	}
}

func TestQueuedHistoryLeavesUnreadStateAlone(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "555", Server: types.DefaultUserServer}
	base := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	if err := a.db.UpsertChat(chat.String(), "dm", "Alice", base.Add(time.Hour)); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := a.db.SetChatUnreadCount(chat.String(), 2); err != nil {
		t.Fatalf("SetChatUnreadCount: %v", err)
	}
	histEvt, _ := historyNotificationEvent("hist-old", waE2E.HistorySyncType_FULL)
	f.connectEvents = []any{histEvt}
	f.downloadHistory = func(*waE2E.HistorySyncNotification) (*waHistorySync.HistorySync, error) {
		data := historySyncWithTextMessages(chat, base, "m-old").Data
		// Built by the phone before the two live messages arrived.
		data.Conversations[0].UnreadCount = proto.Uint32(0)
		return data, nil
	}

	if _, err := a.Sync(context.Background(), SyncOptions{Mode: SyncModeOnce, IdleExit: 10 * time.Millisecond}); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	got, err := a.db.GetChat(chat.String())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if got.UnreadCount != 2 {
		t.Fatalf("unread = %d, want 2: a queued chunk must not replace newer unread state", got.UnreadCount)
	}
}

func TestBootstrapHistoryStillSetsUnreadState(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "555", Server: types.DefaultUserServer}
	base := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	histEvt, _ := historyNotificationEvent("hist-boot", waE2E.HistorySyncType_INITIAL_BOOTSTRAP)
	f.connectEvents = []any{histEvt}
	f.downloadHistory = func(*waE2E.HistorySyncNotification) (*waHistorySync.HistorySync, error) {
		data := historySyncWithTextMessages(chat, base, "m-boot").Data
		data.Conversations[0].UnreadCount = proto.Uint32(3)
		return data, nil
	}

	if _, err := a.Sync(context.Background(), SyncOptions{Mode: SyncModeOnce, IdleExit: 10 * time.Millisecond}); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	got, err := a.db.GetChat(chat.String())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if got.UnreadCount != 3 {
		t.Fatalf("unread = %d, want 3 from the bootstrap chunk", got.UnreadCount)
	}
	if n, err := a.db.CountHistorySyncQueue(); err != nil || n != 0 {
		t.Fatalf("queue = %d (err %v): the bootstrap chunk must not be queued", n, err)
	}
}

func TestHistoryQueueRetriesThenGivesUp(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	previous := historyQueueRetryDelay
	historyQueueRetryDelay = time.Millisecond
	t.Cleanup(func() { historyQueueRetryDelay = previous })

	q := newHistoryQueue(a)
	_, notif := historyNotificationEvent("hist-bad", waE2E.HistorySyncType_FULL)
	if err := q.enqueue("hist-bad", notif); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	var calls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		q.run(ctx, func(context.Context, store.HistorySyncQueueItem) error {
			calls.Add(1)
			return errors.New("download failed")
		})
	}()
	waitUntil(t, "the failing chunk to be dropped", func() bool {
		n, err := a.db.CountHistorySyncQueue()
		return err == nil && n == 0
	})
	cancel()
	<-done
	if got := calls.Load(); got != historyQueueMaxAttempts {
		t.Fatalf("attempts = %d, want %d", got, historyQueueMaxAttempts)
	}
}

func TestHistoryQueueKeepsAChunkInterruptedByStop(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()

	q := newHistoryQueue(a)
	_, notif := historyNotificationEvent("hist-slow", waE2E.HistorySyncType_FULL)
	if err := q.enqueue("hist-slow", notif); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		q.run(ctx, func(ctx context.Context, _ store.HistorySyncQueueItem) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	<-started
	cancel()
	<-done

	item, ok, err := a.db.NextHistorySync()
	if err != nil || !ok {
		t.Fatalf("NextHistorySync = %v, %v: the interrupted chunk must stay queued", ok, err)
	}
	if item.MsgID != "hist-slow" || item.Attempts != 0 {
		t.Fatalf("queued item = %+v, want hist-slow with no failed attempt", item)
	}
}

func TestEnqueueHistorySyncKeepsOneCopyPerMessage(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()

	q := newHistoryQueue(a)
	_, notif := historyNotificationEvent("", waE2E.HistorySyncType_RECENT)
	for _, id := range []string{"hist-dup", "hist-dup", "", ""} {
		if err := q.enqueue(id, notif); err != nil {
			t.Fatalf("enqueue %q: %v", id, err)
		}
	}
	n, err := a.db.CountHistorySyncQueue()
	if err != nil {
		t.Fatalf("CountHistorySyncQueue: %v", err)
	}
	if n != 3 {
		t.Fatalf("queued = %d, want 3: a redelivered notification once, and every one without an ID", n)
	}
}

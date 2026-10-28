package app

import (
	"bytes"
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/out"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// A message is acked to WhatsApp only after it is stored. whatsmeow withholds the
// receipt when a success-status handler returns false, so the server can
// redeliver it.

type ackTestSetup struct {
	opts   SyncOptions
	limits func(a *App, cancel context.CancelFunc) *syncStorageLimits
	mediaQ *mediaQueue
}

func ackTestHandler(t *testing.T, setup ackTestSetup) (*App, *fakeWA, *bytes.Buffer, *syncStorageLimits) {
	t.Helper()
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	var evbuf bytes.Buffer
	a.opts.Events = out.NewEventWriter(&evbuf, true)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var limits *syncStorageLimits
	if setup.limits != nil {
		limits = setup.limits(a, cancel)
	}
	var messagesStored, lastEvent atomic.Int64
	a.addSyncEventHandler(ctx, setup.opts, &messagesStored, &lastEvent,
		make(chan struct{}, 1), make(chan struct{}, 1), make(chan staleReconnectRequest, 1),
		func(string, string) {}, nil, limits, &syncPresence{}, setup.mediaQ)
	return a, f, &evbuf, limits
}

func ackTestChat() types.JID {
	return types.JID{User: "15551234567", Server: types.DefaultUserServer}
}

func ackTestMessage(id string) *events.Message {
	chat := ackTestChat()
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat},
			ID:            types.MessageID(id),
			Timestamp:     time.Date(2026, 10, 28, 4, 4, 54, 0, time.UTC),
			PushName:      "Alice",
		},
		Message: &waProto.Message{Conversation: proto.String("hello")},
	}
}

func ackTestHistoryNotification(t *testing.T, f *fakeWA, id string, msgIDs ...string) *events.Message {
	t.Helper()
	if len(msgIDs) > 0 {
		f.downloadHistory = func(*waE2E.HistorySyncNotification) (*waHistorySync.HistorySync, error) {
			return historySyncWithTextMessages(ackTestChat(), time.Date(2026, 10, 28, 4, 0, 0, 0, time.UTC), msgIDs...).Data, nil
		}
	}
	syncType := waE2E.HistorySyncType_RECENT
	return &events.Message{
		Info: ackTestMessage(id).Info,
		Message: &waProto.Message{ProtocolMessage: &waProto.ProtocolMessage{
			HistorySyncNotification: &waE2E.HistorySyncNotification{SyncType: &syncType}}},
	}
}

func messageCapLimits(max int64) func(*App, context.CancelFunc) *syncStorageLimits {
	return func(a *App, cancel context.CancelFunc) *syncStorageLimits {
		opts := SyncOptions{MaxMessages: max}
		return &syncStorageLimits{app: a, opts: opts, cancel: cancel}
	}
}

func TestSyncHandlerAcksAStoredMessage(t *testing.T) {
	a, f, _, _ := ackTestHandler(t, ackTestSetup{})
	if !f.emitForAck(ackTestMessage("m1")) {
		t.Fatal("stored message was refused; want ack")
	}
	if n, _ := a.db.CountMessages(); n != 1 {
		t.Fatalf("messages = %d, want 1", n)
	}
}

func TestSyncHandlerRefusesAckAndReportsWhenStoreFails(t *testing.T) {
	a, f, evbuf, _ := ackTestHandler(t, ackTestSetup{})
	_ = a.db.Close()
	if f.emitForAck(ackTestMessage("m2")) {
		t.Fatal("message acked although the store failed; it would be lost")
	}
	if !strings.Contains(evbuf.String(), `"event":"store_failed"`) || !strings.Contains(evbuf.String(), "m2") {
		t.Fatalf("no store_failed event naming the message: %s", evbuf.String())
	}
}

func TestSyncHandlerRefusesAckAndMarksPersistenceFailedWhenStoreFailsUncapped(t *testing.T) {
	a, f, _, limits := ackTestHandler(t, ackTestSetup{limits: messageCapLimits(0)})
	_ = a.db.Close()
	if f.emitForAck(ackTestMessage("m2")) {
		t.Fatal("message acked although the store failed; it would be lost")
	}
	if !limits.persistenceFailed.Load() {
		t.Fatal("a failed store must still keep the sync from recording freshness")
	}
}

func TestSyncHandlerAcksAMessageDroppedByTheStorageCap(t *testing.T) {
	a, f, evbuf, limits := ackTestHandler(t, ackTestSetup{limits: messageCapLimits(1)})
	if !f.emitForAck(ackTestMessage("m1")) {
		t.Fatal("first message under the cap was refused")
	}
	if !f.emitForAck(ackTestMessage("m2")) {
		t.Fatal("a message dropped by the user's cap must still be acked")
	}
	if limits.Err() == nil {
		t.Fatal("cap was not reached")
	}
	if n, _ := a.db.CountMessages(); n != 1 {
		t.Fatalf("messages = %d, want 1", n)
	}
	if strings.Contains(evbuf.String(), `"event":"store_failed"`) {
		t.Fatalf("a deliberate cap drop is not a store failure: %s", evbuf.String())
	}
}

func TestSyncHandlerRefusesAckWhenTheStorageCapCannotBeChecked(t *testing.T) {
	a, f, _, limits := ackTestHandler(t, ackTestSetup{limits: messageCapLimits(10)})
	_ = a.db.Close()
	if f.emitForAck(ackTestMessage("m2")) {
		t.Fatal("message acked although the cap check failed; a failed check is not the user's cap")
	}
	if limits.Err() == nil {
		t.Fatal("cap check did not fail; the test does not exercise the check-failure path")
	}
}

func TestSyncHandlerRefusesAckWhenItPanicsOnAMessage(t *testing.T) {
	_, f, _, _ := ackTestHandler(t, ackTestSetup{})
	f.decryptReactionPanic = "boom"
	evt := ackTestMessage("m3")
	evt.Message = &waProto.Message{EncReactionMessage: &waProto.EncReactionMessage{
		TargetMessageKey: &waCommon.MessageKey{ID: proto.String("m1")}}}
	if f.emitForAck(evt) {
		t.Fatal("a message whose handler panicked was acked; it would be lost")
	}
	if !f.emitForAck(&events.Receipt{}) {
		t.Fatal("after a panic, unrelated events must still be acked")
	}
}

func TestSyncHandlerRefusesAMessageWhileShuttingDown(t *testing.T) {
	q := newMediaQueue(1)
	q.accepting = false
	a, f, _, _ := ackTestHandler(t, ackTestSetup{mediaQ: q})
	if f.emitForAck(ackTestMessage("m5")) {
		t.Fatal("a message that arrived during shutdown was acked although it was not stored")
	}
	if n, _ := a.db.CountMessages(); n != 0 {
		t.Fatalf("messages = %d, want 0", n)
	}
	if !f.emitForAck(&events.Receipt{}) {
		t.Fatal("non-message events during shutdown must still be acked")
	}
}

func TestSyncHandlerKeepsAHistoryChunkWhoseStoreFailed(t *testing.T) {
	a, f, evbuf, _ := ackTestHandler(t, ackTestSetup{})
	notif := ackTestHistoryNotification(t, f, "n1", "h1", "h2")
	_ = a.db.Close()
	if f.emitForAck(notif) {
		t.Fatal("history notification acked although its messages were not stored")
	}
	if len(f.deleteHistoryCalls) != 0 {
		t.Fatal("history blob deleted from the server although its messages were not stored")
	}
	if !strings.Contains(evbuf.String(), `"event":"store_failed"`) || !strings.Contains(evbuf.String(), "h1") {
		t.Fatalf("no store_failed event naming the history message: %s", evbuf.String())
	}
}

func TestSyncHandlerAcksAndDeletesAStoredHistoryChunk(t *testing.T) {
	a, f, _, _ := ackTestHandler(t, ackTestSetup{})
	notif := ackTestHistoryNotification(t, f, "n2", "h1", "h2")
	if !f.emitForAck(notif) {
		t.Fatal("stored history chunk was refused")
	}
	if n, _ := a.db.CountMessages(); n != 2 {
		t.Fatalf("messages = %d, want 2", n)
	}
	if len(f.deleteHistoryCalls) != 1 {
		t.Fatalf("delete calls = %d, want 1", len(f.deleteHistoryCalls))
	}
}

func TestSyncHandlerAcksAHistoryChunkCutByTheStorageCap(t *testing.T) {
	a, f, evbuf, limits := ackTestHandler(t, ackTestSetup{limits: messageCapLimits(1)})
	notif := ackTestHistoryNotification(t, f, "n4", "h1", "h2", "h3")
	if !f.emitForAck(notif) {
		t.Fatal("a history chunk cut by the user's cap must still be acked")
	}
	if limits.Err() == nil {
		t.Fatal("cap was not reached")
	}
	if n, _ := a.db.CountMessages(); n != 1 {
		t.Fatalf("messages = %d, want 1", n)
	}
	if strings.Contains(evbuf.String(), `"event":"store_failed"`) {
		t.Fatalf("a deliberate cap drop is not a store failure: %s", evbuf.String())
	}
}

func TestSyncHandlerRefusesAHistoryChunkThatFailedToDownload(t *testing.T) {
	_, f, _, _ := ackTestHandler(t, ackTestSetup{})
	notif := ackTestHistoryNotification(t, f, "n3") // fake download returns "not supported"
	if f.emitForAck(notif) {
		t.Fatal("history notification acked although the download failed")
	}
	if len(f.deleteHistoryCalls) != 0 {
		t.Fatal("history blob deleted although it never downloaded")
	}
}

func TestSyncHandlerRefusesAHistoryChunkInterruptedByShutdown(t *testing.T) {
	var stop context.CancelFunc
	a, f, _, _ := ackTestHandler(t, ackTestSetup{limits: func(_ *App, cancel context.CancelFunc) *syncStorageLimits {
		stop = cancel
		return nil
	}})
	notif := ackTestHistoryNotification(t, f, "n5", "h1", "h2")
	download := f.downloadHistory
	f.downloadHistory = func(n *waE2E.HistorySyncNotification) (*waHistorySync.HistorySync, error) {
		data, err := download(n)
		// Shutdown lands mid-chunk: the store fails and sync is cancelled,
		// not by the user's cap.
		stop()
		_ = a.db.Close()
		return data, err
	}
	if f.emitForAck(notif) {
		t.Fatal("history chunk acked although shutdown interrupted it before it was stored")
	}
	if len(f.deleteHistoryCalls) != 0 {
		t.Fatal("history blob deleted although its messages were not stored")
	}
}

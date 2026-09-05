package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/out"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// collectWebhookEvents drives the real sync event handler and records every
// event it enqueues for delivery.
type webhookEventRecorder struct {
	mu     sync.Mutex
	events []syncWebhookEvent
}

func (r *webhookEventRecorder) enqueue(evt syncWebhookEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, evt)
}

func (r *webhookEventRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

func offlineTestApp(t *testing.T, rec *webhookEventRecorder) (*App, *fakeWA) {
	t.Helper()
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	a.opts.Events = out.NewEventWriter(io.Discard, true)

	var messagesStored atomic.Int64
	var lastEvent atomic.Int64
	a.addSyncEventHandler(
		context.Background(),
		SyncOptions{},
		&messagesStored,
		&lastEvent,
		make(chan struct{}, 1),
		make(chan struct{}, 1),
		make(chan staleReconnectRequest, 1),
		func(string, string) {},
		rec.enqueue,
		nil,
		&syncPresence{},
		nil,
	)
	return a, f
}

func offlineTestMessage(id string) *events.Message {
	chat := types.JID{User: "15551234567", Server: types.DefaultUserServer}
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat},
			ID:            id,
			Timestamp:     time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		},
		Message: &waProto.Message{Conversation: proto.String("hello")},
	}
}

// The lifecycle events are the half a consumer that does not use --webhook can
// act on: they name the replay, and how much of it is still coming.
func TestOfflineSyncEmitsLifecycleEvents(t *testing.T) {
	var eventsOut bytes.Buffer
	rec := &webhookEventRecorder{}
	a, f := offlineTestApp(t, rec)
	a.opts.Events = out.NewEventWriter(&eventsOut, true)

	f.emit(&events.OfflineSyncPreview{Total: 7, Messages: 4, Receipts: 2, Notifications: 1})
	f.emit(&events.OfflineSyncCompleted{Count: 7})

	log := eventsOut.String()
	for _, want := range []string{
		`"event":"offline_sync_preview"`,
		`"messages":4`,
		`"receipts":2`,
		`"total":7`,
		`"event":"offline_sync_completed"`,
		`"count":7`,
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("event log missing %s: %s", want, log)
		}
	}
}

// The replay budget is consumed when a message ARRIVES, not when it is queued
// for delivery: the live path enqueues only after storage succeeds, so counting
// at the enqueuer would leave a failed message's slot open, and the next live
// message would be published as backlog.
// The handler is registered once for the whole sync run, so a replay cut short
// by a dropped socket would otherwise leave slots behind for the reconnect's
// live traffic to spend.
// StreamReplaced is the other way a connection ends mid-replay.
// Removing the payload marker means a replayed message must be delivered in
// exactly the shape a live one has: the lifecycle events are the only new
// surface, and strict decoders see no change at all.
func TestReplayedMessagePayloadIsUnchanged(t *testing.T) {
	rec := &webhookEventRecorder{}
	a, f := offlineTestApp(t, rec)

	f.emit(&events.OfflineSyncPreview{Total: 1, Messages: 1})
	f.emit(offlineTestMessage("replayed-1"))
	f.emit(&events.OfflineSyncCompleted{Count: 1})
	f.emit(offlineTestMessage("live-1"))

	if rec.count() != 2 {
		t.Fatalf("enqueued %d webhook events, want 2", rec.count())
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for i, evt := range rec.events {
		body, err := json.Marshal(a.newSyncWebhookPayload(context.Background(), evt.Message))
		if err != nil {
			t.Fatalf("marshal %d: %v", i, err)
		}
		if strings.Contains(string(body), "Offline") {
			t.Fatalf("payload %d carries an Offline key: %s", i, body)
		}
	}
}

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

// Replay signals must not change the webhook schema for strict decoders.
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

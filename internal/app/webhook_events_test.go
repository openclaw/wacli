package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/wa"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestParseSyncWebhookEvents(t *testing.T) {
	t.Run("default is message only", func(t *testing.T) {
		set, err := ParseSyncWebhookEvents("message")
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if !set.Enabled(SyncWebhookEventMessage) {
			t.Fatal("message should be enabled")
		}
		for _, kind := range []SyncWebhookEventKind{SyncWebhookEventReceipt, SyncWebhookEventChatPresence} {
			if set.Enabled(kind) {
				t.Fatalf("%s should be disabled by default", kind)
			}
		}
	})

	t.Run("empty value falls back to message", func(t *testing.T) {
		set, err := ParseSyncWebhookEvents("")
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if !set.Enabled(SyncWebhookEventMessage) || set.Enabled(SyncWebhookEventReceipt) {
			t.Fatalf("unexpected set %v", set)
		}
	})

	t.Run("zero value behaves like the default", func(t *testing.T) {
		var set SyncWebhookEventSet
		if !set.Enabled(SyncWebhookEventMessage) || set.Enabled(SyncWebhookEventChatPresence) {
			t.Fatal("nil set must forward messages only")
		}
	})

	t.Run("all three kinds", func(t *testing.T) {
		set, err := ParseSyncWebhookEvents(" message , receipt ,chat_presence")
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		for _, kind := range []SyncWebhookEventKind{SyncWebhookEventMessage, SyncWebhookEventReceipt, SyncWebhookEventChatPresence} {
			if !set.Enabled(kind) {
				t.Fatalf("%s should be enabled", kind)
			}
		}
	})

	t.Run("receipts without messages", func(t *testing.T) {
		set, err := ParseSyncWebhookEvents("receipt")
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if set.Enabled(SyncWebhookEventMessage) {
			t.Fatal("message should not be implied")
		}
	})

	t.Run("unknown kind is rejected", func(t *testing.T) {
		if _, err := ParseSyncWebhookEvents("message,presence"); err == nil {
			t.Fatal("expected error for unknown event kind")
		}
	})
}

func TestNewSyncWebhookReceiptEventFiltersNoise(t *testing.T) {
	chat := types.JID{User: "120363000000000000", Server: types.GroupServer}
	sender := types.JID{User: "15551234567", Server: types.DefaultUserServer}
	base := func(receiptType types.ReceiptType) *events.Receipt {
		return &events.Receipt{
			MessageSource: types.MessageSource{Chat: chat, Sender: sender, IsGroup: true},
			MessageIDs:    []types.MessageID{"m-1"},
			Timestamp:     time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC),
			Type:          receiptType,
		}
	}

	for _, receiptType := range []types.ReceiptType{
		types.ReceiptTypeSender,
		types.ReceiptTypeRetry,
		types.ReceiptTypeReadSelf,
		types.ReceiptTypePlayedSelf,
		types.ReceiptTypeInactive,
		types.ReceiptTypeServerError,
		types.ReceiptTypePeerMsg,
		types.ReceiptTypeHistorySync,
	} {
		if _, ok := newSyncWebhookReceiptEvent(base(receiptType)); ok {
			t.Fatalf("receipt type %q should not cross the webhook", receiptType)
		}
	}

	for receiptType, want := range map[types.ReceiptType]string{
		types.ReceiptTypeDelivered: "delivered",
		types.ReceiptTypeRead:      "read",
		types.ReceiptTypePlayed:    "played",
	} {
		evt, ok := newSyncWebhookReceiptEvent(base(receiptType))
		if !ok {
			t.Fatalf("receipt type %q should be forwarded", receiptType)
		}
		if evt.Receipt.Type != want {
			t.Fatalf("receipt type = %q, want %q", evt.Receipt.Type, want)
		}
		if evt.Receipt.Sender != sender {
			t.Fatalf("receipt sender = %s, want the group participant %s", evt.Receipt.Sender, sender)
		}
	}

	if _, ok := newSyncWebhookReceiptEvent(&events.Receipt{
		MessageSource: types.MessageSource{Chat: chat},
		Type:          types.ReceiptTypeRead,
	}); ok {
		t.Fatal("receipt without message IDs should be dropped")
	}
}

func TestNewSyncWebhookReceiptEventKeepsBatches(t *testing.T) {
	evt, ok := newSyncWebhookReceiptEvent(&events.Receipt{
		MessageSource: types.MessageSource{Chat: types.JID{User: "15551234567", Server: types.DefaultUserServer}},
		MessageIDs:    []types.MessageID{"m-1", "", "m-2"},
		Type:          types.ReceiptTypeRead,
	})
	if !ok {
		t.Fatal("receipt should be forwarded")
	}
	if len(evt.Receipt.MessageIDs) != 2 {
		t.Fatalf("message IDs = %v, want the batch intact minus blanks", evt.Receipt.MessageIDs)
	}
}

func TestSyncWebhookPayloadShapes(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()

	receipt, ok := newSyncWebhookReceiptEvent(&events.Receipt{
		MessageSource: types.MessageSource{
			Chat:     types.JID{User: "120363000000000000", Server: types.GroupServer},
			Sender:   types.JID{User: "15551234567", Server: types.DefaultUserServer},
			IsFromMe: false,
			IsGroup:  true,
		},
		MessageIDs: []types.MessageID{"m-1"},
		Timestamp:  time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC),
		Type:       types.ReceiptTypeDelivered,
	})
	if !ok {
		t.Fatal("receipt should be forwarded")
	}
	body, err := json.Marshal(a.newSyncWebhookEventPayload(ctx, receipt))
	if err != nil {
		t.Fatalf("marshal receipt: %v", err)
	}
	for _, want := range []string{
		`"EventType":"receipt"`,
		`"Chat":"120363000000000000@g.us"`,
		`"Sender":"15551234567@s.whatsapp.net"`,
		`"MessageIDs":["m-1"]`,
		`"Type":"delivered"`,
		`"IsFromMe":false`,
	} {
		if !bytes.Contains(body, []byte(want)) {
			t.Fatalf("receipt payload missing %s: %s", want, body)
		}
	}

	presence, ok := newSyncWebhookChatPresenceEvent(&events.ChatPresence{
		MessageSource: types.MessageSource{
			Chat:   types.JID{User: "120363000000000000", Server: types.GroupServer},
			Sender: types.JID{User: "15551234567", Server: types.DefaultUserServer},
		},
		State: types.ChatPresenceComposing,
		Media: types.ChatPresenceMediaAudio,
	})
	if !ok {
		t.Fatal("chat presence should be forwarded")
	}
	body, err = json.Marshal(a.newSyncWebhookEventPayload(ctx, presence))
	if err != nil {
		t.Fatalf("marshal chat presence: %v", err)
	}
	for _, want := range []string{
		`"EventType":"chat_presence"`,
		`"State":"composing"`,
		`"Media":"audio"`,
	} {
		if !bytes.Contains(body, []byte(want)) {
			t.Fatalf("chat presence payload missing %s: %s", want, body)
		}
	}
}

func TestMessageWebhookPayloadMatchesLegacyBody(t *testing.T) {
	a := newTestApp(t)
	message := wa.ParsedMessage{ID: "m-legacy", Text: "hello"}

	got, err := json.Marshal(a.newSyncWebhookEventPayload(context.Background(), syncWebhookEvent{
		Kind:    SyncWebhookEventMessage,
		Message: message,
	}))
	if err != nil {
		t.Fatalf("marshal message event: %v", err)
	}
	legacy, err := json.Marshal(struct {
		wa.ParsedMessage
		ChatName string `json:"ChatName,omitempty"`
	}{ParsedMessage: message})
	if err != nil {
		t.Fatalf("marshal legacy message: %v", err)
	}
	if !bytes.Equal(got, legacy) {
		t.Fatalf("message payload changed\ngot:  %s\nwant: %s", got, legacy)
	}
}

func TestPostSyncWebhookEventSignsNewEventKinds(t *testing.T) {
	events := []syncWebhookEvent{
		{
			Kind: SyncWebhookEventReceipt,
			Receipt: syncWebhookReceipt{
				Chat:       types.JID{User: "120363000000000000", Server: types.GroupServer},
				MessageIDs: []string{"m-1"},
				Type:       "read",
			},
		},
		{
			Kind: SyncWebhookEventChatPresence,
			Presence: syncWebhookChatPresence{
				Chat:  types.JID{User: "15551234567", Server: types.DefaultUserServer},
				State: "composing",
			},
		},
	}

	for _, event := range events {
		event := event
		t.Run(string(event.Kind), func(t *testing.T) {
			var body []byte
			var signature string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var err error
				body, err = io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read body: %v", err)
				}
				signature = r.Header.Get("X-Wacli-Signature")
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()

			a := newTestApp(t)
			err := a.postSyncWebhookEvent(context.Background(), SyncOptions{
				WebhookURL:          server.URL,
				WebhookSecret:       "test-secret",
				WebhookAllowPrivate: true,
			}, event)
			if err != nil {
				t.Fatalf("post event: %v", err)
			}
			if want := syncWebhookSignature("test-secret", body); signature != want {
				t.Fatalf("signature = %q, want %q", signature, want)
			}
		})
	}
}

// syncWebhookRecorder collects webhook bodies for the handler-level tests.
type syncWebhookRecorder struct {
	srv    *httptest.Server
	bodies chan []byte
}

func newSyncWebhookRecorder(t *testing.T) *syncWebhookRecorder {
	t.Helper()
	rec := &syncWebhookRecorder{bodies: make(chan []byte, 8)}
	rec.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("ReadAll: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		rec.bodies <- body
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(rec.srv.Close)
	return rec
}

func (r *syncWebhookRecorder) next(t *testing.T) []byte {
	t.Helper()
	select {
	case body := <-r.bodies:
		return body
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for webhook request")
		return nil
	}
}

func (r *syncWebhookRecorder) expectSilence(t *testing.T) {
	t.Helper()
	select {
	case body := <-r.bodies:
		t.Fatalf("unexpected webhook request: %s", body)
	case <-time.After(200 * time.Millisecond):
	}
}

func emitWebhookEvents(t *testing.T, webhookEvents string, emit func(f *fakeWA)) *syncWebhookRecorder {
	t.Helper()
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	rec := newSyncWebhookRecorder(t)
	set, err := ParseSyncWebhookEvents(webhookEvents)
	if err != nil {
		t.Fatalf("parse webhook events: %v", err)
	}
	opts := SyncOptions{
		Mode:                SyncModeFollow,
		WebhookURL:          rec.srv.URL,
		WebhookAllowPrivate: true,
		WebhookEvents:       set,
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	jobs := make(chan syncWebhookEvent, 8)
	stopWebhook := a.runSyncWebhookWorker(ctx, opts, jobs)
	t.Cleanup(stopWebhook)

	var messagesStored, lastEvent atomic.Int64
	handlerID := a.addSyncEventHandler(
		ctx,
		opts,
		&messagesStored,
		&lastEvent,
		make(chan struct{}, 1),
		make(chan staleReconnectRequest, 1),
		func(string, string) {},
		a.newSyncWebhookEnqueuer(ctx, jobs),
		nil,
		&syncPresence{},
		nil,
	)
	t.Cleanup(func() { f.RemoveEventHandler(handlerID) })

	emit(f)
	return rec
}

func testReceiptEvent() *events.Receipt {
	return &events.Receipt{
		MessageSource: types.MessageSource{
			Chat:   types.JID{User: "15551234567", Server: types.DefaultUserServer},
			Sender: types.JID{User: "15551234567", Server: types.DefaultUserServer},
		},
		MessageIDs: []types.MessageID{"m-1"},
		Timestamp:  time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC),
		Type:       types.ReceiptTypeRead,
	}
}

func testChatPresenceEvent() *events.ChatPresence {
	return &events.ChatPresence{
		MessageSource: types.MessageSource{
			Chat:   types.JID{User: "15551234567", Server: types.DefaultUserServer},
			Sender: types.JID{User: "15551234567", Server: types.DefaultUserServer},
		},
		State: types.ChatPresenceComposing,
	}
}

func testLiveMessageEvent() *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:   types.JID{User: "15551234567", Server: types.DefaultUserServer},
				Sender: types.JID{User: "15551234567", Server: types.DefaultUserServer},
			},
			ID:        "m-live",
			Timestamp: time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC),
		},
		Message: &waProto.Message{Conversation: proto.String("hello")},
	}
}

func TestDefaultWebhookEventsForwardMessagesOnly(t *testing.T) {
	rec := emitWebhookEvents(t, "message", func(f *fakeWA) {
		f.emit(testReceiptEvent())
		f.emit(testChatPresenceEvent())
	})
	rec.expectSilence(t)

	rec = emitWebhookEvents(t, "message", func(f *fakeWA) {
		f.emit(testLiveMessageEvent())
	})
	body := rec.next(t)
	if bytes.Contains(body, []byte(`"EventType"`)) {
		t.Fatalf("legacy message payload gained EventType: %s", body)
	}
	for _, want := range []string{`"ID":"m-live"`, `"Text":"hello"`} {
		if !bytes.Contains(body, []byte(want)) {
			t.Fatalf("message payload missing %s: %s", want, body)
		}
	}
}

func TestWebhookEventsOptInDeliversAllThreeKinds(t *testing.T) {
	rec := emitWebhookEvents(t, "message,receipt,chat_presence", func(f *fakeWA) {
		f.emit(testLiveMessageEvent())
		f.emit(testReceiptEvent())
		f.emit(testChatPresenceEvent())
	})

	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		var payload struct {
			EventType string
		}
		body := rec.next(t)
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("unmarshal %s: %v", body, err)
		}
		kind := payload.EventType
		if kind == "" {
			if !bytes.Contains(body, []byte(`"ID":"m-live"`)) {
				t.Fatalf("payload without EventType is not a message: %s", body)
			}
			kind = string(SyncWebhookEventMessage)
		}
		seen[kind] = true
	}
	for _, want := range []string{"message", "receipt", "chat_presence"} {
		if !seen[want] {
			t.Fatalf("missing %s event, saw %v", want, seen)
		}
	}
}

func TestWebhookEventsWithoutMessageStopsForwardingMessages(t *testing.T) {
	rec := emitWebhookEvents(t, "receipt", func(f *fakeWA) {
		f.emit(testLiveMessageEvent())
		f.emit(testReceiptEvent())
	})

	body := rec.next(t)
	if !bytes.Contains(body, []byte(`"EventType":"receipt"`)) {
		t.Fatalf("first payload should be the receipt, got %s", body)
	}
	rec.expectSilence(t)
}

// events.Presence (online / last seen) is deliberately out of scope: only
// per-chat typing state is forwarded.
func TestGlobalPresenceIsNeverForwarded(t *testing.T) {
	rec := emitWebhookEvents(t, "message,receipt,chat_presence", func(f *fakeWA) {
		f.emit(&events.Presence{
			From:        types.JID{User: "15551234567", Server: types.DefaultUserServer},
			Unavailable: false,
		})
	})
	rec.expectSilence(t)
}

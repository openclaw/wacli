package app

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steipete/wacli/internal/wa"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestHandleLiveSyncMessagePostsSignedWebhook(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	type requestInfo struct {
		body        []byte
		signature   string
		contentType string
	}
	gotReq := make(chan requestInfo, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll: %v", err)
		}
		gotReq <- requestInfo{
			body:        body,
			signature:   r.Header.Get("X-Wacli-Signature"),
			contentType: r.Header.Get("Content-Type"),
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	chat := types.JID{User: "15551234567", Server: types.DefaultUserServer}
	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:     chat,
				Sender:   chat,
				IsFromMe: false,
				IsGroup:  false,
			},
			ID:        "m-live",
			Timestamp: time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC),
			PushName:  "Alice",
		},
		Message: &waProto.Message{Conversation: proto.String("hello")},
	}

	var messagesStored atomic.Int64
	jobs := make(chan wa.ParsedMessage, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopWebhook := a.runSyncWebhookWorker(ctx, SyncOptions{
		WebhookURL:    srv.URL,
		WebhookSecret: "supersecret",
	}, jobs)
	defer stopWebhook()

	a.handleLiveSyncMessage(context.Background(), SyncOptions{
		WebhookURL:    srv.URL,
		WebhookSecret: "supersecret",
	}, evt, &messagesStored, func(string, string) {}, a.newSyncWebhookEnqueuer(ctx, jobs))

	if messagesStored.Load() != 1 {
		t.Fatalf("messages stored = %d, want 1", messagesStored.Load())
	}

	var got requestInfo
	select {
	case got = <-gotReq:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for webhook request")
	}
	if got.contentType != "application/json" {
		t.Fatalf("content type = %q", got.contentType)
	}
	if got.signature != syncWebhookSignature("supersecret", got.body) {
		t.Fatalf("signature = %q, want %q", got.signature, syncWebhookSignature("supersecret", got.body))
	}
	for _, want := range [][]byte{[]byte(`"ID":"m-live"`), []byte(`"Text":"hello"`)} {
		if !bytes.Contains(got.body, want) {
			t.Fatalf("webhook body missing %s: %s", want, got.body)
		}
	}
}

func TestHandleLiveSyncMessageDoesNotBlockOnWebhookDelivery(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-releaseRequest
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	jobs := make(chan wa.ParsedMessage, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopWebhook := a.runSyncWebhookWorker(ctx, SyncOptions{WebhookURL: srv.URL}, jobs)
	defer stopWebhook()

	chat := types.JID{User: "15551234567", Server: types.DefaultUserServer}
	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat},
			ID:            "m-slow-webhook",
			Timestamp:     time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC),
		},
		Message: &waProto.Message{Conversation: proto.String("hello")},
	}

	var messagesStored atomic.Int64
	returned := make(chan struct{})
	go func() {
		a.handleLiveSyncMessage(context.Background(), SyncOptions{WebhookURL: srv.URL}, evt, &messagesStored, func(string, string) {}, a.newSyncWebhookEnqueuer(ctx, jobs))
		close(returned)
	}()

	select {
	case <-returned:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("live message handler blocked on webhook delivery")
	}
	if messagesStored.Load() != 1 {
		t.Fatalf("messages stored = %d, want 1", messagesStored.Load())
	}
	select {
	case <-requestStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for webhook worker request")
	}
	close(releaseRequest)
}

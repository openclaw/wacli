package app

import (
	"bytes"
	"context"
	"errors"
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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestHandleLiveSyncMessagePostsSignedWebhookWithGroupName(t *testing.T) {
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

	chat := types.JID{User: "120363000000000000", Server: types.GroupServer}
	f.groups[chat] = &types.GroupInfo{
		JID:       chat,
		GroupName: types.GroupName{Name: "test1"},
	}
	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:     chat,
				Sender:   types.JID{User: "15551234567", Server: types.DefaultUserServer},
				IsFromMe: false,
				IsGroup:  true,
			},
			ID:        "m-live",
			Timestamp: time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC),
			PushName:  "",
		},
		Message: &waProto.Message{Conversation: proto.String("hello")},
	}

	var messagesStored atomic.Int64
	jobs := make(chan wa.ParsedMessage, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopWebhook := a.runSyncWebhookWorker(ctx, SyncOptions{
		WebhookURL:          srv.URL,
		WebhookSecret:       "supersecret",
		WebhookAllowPrivate: true,
	}, jobs)
	defer stopWebhook()

	a.handleLiveSyncMessage(context.Background(), SyncOptions{
		WebhookURL:          srv.URL,
		WebhookSecret:       "supersecret",
		WebhookAllowPrivate: true,
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
	for _, want := range [][]byte{
		[]byte(`"ChatName":"test1"`),
		[]byte(`"ID":"m-live"`),
		[]byte(`"Text":"hello"`),
	} {
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
	stopWebhook := a.runSyncWebhookWorker(ctx, SyncOptions{WebhookURL: srv.URL, WebhookAllowPrivate: true}, jobs)
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
		a.handleLiveSyncMessage(context.Background(), SyncOptions{WebhookURL: srv.URL, WebhookAllowPrivate: true}, evt, &messagesStored, func(string, string) {}, a.newSyncWebhookEnqueuer(ctx, jobs))
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

func TestPostSyncWebhookRejectsLocalhostByDefault(t *testing.T) {
	a := newTestApp(t)
	requested := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested <- struct{}{}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	err := a.postSyncWebhook(context.Background(), SyncOptions{WebhookURL: srv.URL}, wa.ParsedMessage{ID: "m-local"})
	if err == nil {
		t.Fatal("expected localhost webhook to be rejected")
	}
	select {
	case <-requested:
		t.Fatal("webhook request reached localhost")
	default:
	}
}

func TestPostSyncWebhookUsesRequestTimeout(t *testing.T) {
	a := newTestApp(t)
	oldClient := syncWebhookPrivateHTTPClient
	oldTimeout := syncWebhookRequestTimeout
	t.Cleanup(func() {
		syncWebhookPrivateHTTPClient = oldClient
		syncWebhookRequestTimeout = oldTimeout
	})

	syncWebhookRequestTimeout = 20 * time.Millisecond
	ctxErr := make(chan error, 1)
	syncWebhookPrivateHTTPClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			<-req.Context().Done()
			err := req.Context().Err()
			ctxErr <- err
			return nil, err
		}),
	}

	start := time.Now()
	err := a.postSyncWebhook(context.Background(), SyncOptions{
		WebhookURL:          "https://example.test/hook",
		WebhookAllowPrivate: true,
	}, wa.ParsedMessage{ID: "m-timeout"})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("webhook timeout took %s, want under 1s", elapsed)
	}
	select {
	case err := <-ctxErr:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("request context error = %v, want deadline exceeded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("transport did not observe request context cancellation")
	}
}

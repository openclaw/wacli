package app

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/out"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

type delayedBackfillWA struct {
	*fakeWA
	handlersAdded int
	beforeStore   chan struct{}
	releaseStore  chan struct{}
	delivered     chan struct{}
	response      *events.HistorySync
}

func (f *delayedBackfillWA) AddEventHandler(handler func(any)) uint32 {
	f.handlersAdded++
	handlerOrder := f.handlersAdded
	return f.fakeWA.AddEventHandler(func(evt any) {
		if _, ok := evt.(*events.HistorySync); ok && handlerOrder == f.handlersAdded {
			close(f.beforeStore)
			<-f.releaseStore
		}
		handler(evt)
	})
}

func (f *delayedBackfillWA) RequestHistorySyncOnDemand(ctx context.Context, _ types.MessageInfo, _ int) (types.MessageID, error) {
	go func() {
		defer close(f.delivered)
		f.emit(f.response)
	}()
	select {
	case <-f.beforeStore:
		return "req", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

type backfillStopWriter struct{ stopped chan struct{} }

func (w backfillStopWriter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("no_older_messages_added")) {
		select {
		case w.stopped <- struct{}{}:
		default:
		}
	}
	return len(p), nil
}

func TestBackfillWaitsForResponsePersistence(t *testing.T) {
	a, f, chat, base := newBackfillRetryTest(t, "anchor")
	delayed := &delayedBackfillWA{
		fakeWA: f, beforeStore: make(chan struct{}), releaseStore: make(chan struct{}), delivered: make(chan struct{}),
		response: backfillTestResponse(chat, "older", base.Add(-time.Second)),
	}
	a.wa = delayed
	stopped := make(chan struct{}, 1)
	a.opts.Events = out.NewEventWriter(backfillStopWriter{stopped: stopped}, true)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	released := false
	defer func() {
		if !released {
			close(delayed.releaseStore)
		}
	}()
	type result struct {
		value BackfillResult
		err   error
	}
	done := make(chan result, 1)
	go func() {
		opts := backfillRetryOptions(chat)
		opts.WaitPerRequest = time.Second
		res, err := a.BackfillHistory(ctx, opts)
		done <- result{res, err}
	}()
	select {
	case <-delayed.beforeStore:
	case <-ctx.Done():
		t.Fatal("response did not reach the storage handler")
	}
	select {
	case <-stopped:
		t.Error("backfill stopped before the response was stored")
	case <-time.After(100 * time.Millisecond):
	}
	close(delayed.releaseStore)
	released = true
	<-delayed.delivered
	got := <-done
	if got.err != nil || got.value.MessagesAdded != 1 {
		t.Fatalf("backfill result = %+v, error = %v", got.value, got.err)
	}
}

package app

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types/events"
)

// captureStderr swaps os.Stderr for the duration of fn and returns everything
// written to it. This lets tests observe the panic-recovery log emitted by
// the Sync event handler without touching production code.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	fn()
	_ = w.Close()
	return <-done
}

// TestSyncEventHandlerPanicHasStackAndCounter exercises the event handler
// recovery path by emitting a typed-nil *events.Message (which forces
// wa.ParseLiveMessage to dereference a nil receiver and panic), then
// asserts the recovery log contains the event type, a running counter,
// and a stack trace (#178).
func TestSyncEventHandlerPanicHasStackAndCounter(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	// Two typed-nil *events.Message values panic deep in ParseLiveMessage
	// when the handler reaches its first dereference. Emitting them while
	// Sync is running forces the handler to recover twice, so we can check
	// the counter increments.
	var nilMsg *events.Message
	f.connectEvents = []interface{}{nilMsg, nilMsg}

	out := captureStderr(t, func() {
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(100 * time.Millisecond)
			cancel()
		}()
		if _, err := a.Sync(ctx, SyncOptions{Mode: SyncModeFollow, AllowQR: false}); err != nil {
			t.Fatalf("Sync: %v", err)
		}
	})

	if !strings.Contains(out, "event handler panic (recovered, total=1)") {
		t.Fatalf("expected recovery log for first panic with counter=1, got:\n%s", out)
	}
	if !strings.Contains(out, "event handler panic (recovered, total=2)") {
		t.Fatalf("expected counter to increment to 2 on the second panic, got:\n%s", out)
	}
	if !strings.Contains(out, "event=*events.Message") {
		t.Fatalf("expected event type annotation in recovery log, got:\n%s", out)
	}
	if !strings.Contains(out, "runtime/debug.Stack") && !strings.Contains(out, "sync.go") {
		t.Fatalf("expected stack trace in recovery log, got:\n%s", out)
	}
}

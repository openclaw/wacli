package app

import (
	"context"
	"testing"
	"time"
)

func TestSocketPingPong(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv, err := a.StartSocketServer(ctx, func(req SocketRequest) SocketResponse {
		if req.Action == "ping" {
			return SocketResponse{OK: true}
		}
		return SocketResponse{OK: false, Error: "unknown"}
	})
	if err != nil {
		t.Fatalf("StartSocketServer: %v", err)
	}
	defer srv.Stop()

	// Give the server a moment to start.
	time.Sleep(50 * time.Millisecond)

	if !IsSocketAvailable(a.opts.StoreDir) {
		t.Fatal("expected socket to be available")
	}

	resp, err := SendSocketRequest(a.opts.StoreDir, SocketRequest{Action: "ping"})
	if err != nil {
		t.Fatalf("SendSocketRequest: %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected OK response, got: %+v", resp)
	}
}

func TestSocketSendText(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	f.connected = true
	a.wa = f

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv, err := a.StartSocketServer(ctx, a.makeSocketHandler(ctx))
	if err != nil {
		t.Fatalf("StartSocketServer: %v", err)
	}
	defer srv.Stop()

	time.Sleep(50 * time.Millisecond)

	resp, err := SendSocketRequest(a.opts.StoreDir, SocketRequest{
		Action:  "send_text",
		To:      "123@s.whatsapp.net",
		Message: "hello from socket",
	})
	if err != nil {
		t.Fatalf("SendSocketRequest: %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected OK response, got: %+v", resp)
	}
	if resp.ID == "" {
		t.Fatal("expected non-empty message ID")
	}
}

func TestSocketMarkRead(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	f.connected = true
	a.wa = f

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv, err := a.StartSocketServer(ctx, a.makeSocketHandler(ctx))
	if err != nil {
		t.Fatalf("StartSocketServer: %v", err)
	}
	defer srv.Stop()

	time.Sleep(50 * time.Millisecond)

	resp, err := SendSocketRequest(a.opts.StoreDir, SocketRequest{
		Action:     "mark_read",
		Chat:       "123@s.whatsapp.net",
		MessageIDs: []string{"msg1", "msg2"},
	})
	if err != nil {
		t.Fatalf("SendSocketRequest: %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected OK response, got: %+v", resp)
	}
}

func TestSocketNotAvailableWhenStopped(t *testing.T) {
	a := newTestApp(t)
	if IsSocketAvailable(a.opts.StoreDir) {
		t.Fatal("expected socket to NOT be available")
	}
}

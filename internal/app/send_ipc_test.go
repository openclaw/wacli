package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steipete/wacli/internal/store"
)

// ipcTestDir creates a short temp dir suitable for Unix socket paths
// (macOS limits socket paths to 104 characters).
func ipcTestDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ws")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// newIPCTestApp creates an App with a short store dir for IPC tests.
func newIPCTestApp(t *testing.T) *App {
	t.Helper()
	dir := ipcTestDir(t)
	a, err := New(Options{StoreDir: dir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}

func TestDelegateSendText_RoundTrip(t *testing.T) {
	a := newIPCTestApp(t)
	fw := newFakeWA()
	a.wa = fw

	srv, err := a.startSendDelegateServer()
	if err != nil {
		t.Fatalf("startSendDelegateServer: %v", err)
	}
	defer srv.Close()

	res, err := DelegateSendText(context.Background(), a.opts.StoreDir, SendTextParams{
		To:      "6591234567",
		Message: "hello via IPC",
	})
	if err != nil {
		t.Fatalf("DelegateSendText: %v", err)
	}

	// Verify result.
	wantJID := "6591234567@s.whatsapp.net"
	if res.To != wantJID {
		t.Errorf("To = %q, want %q", res.To, wantJID)
	}
	if res.ID != "msgid" {
		t.Errorf("ID = %q, want %q", res.ID, "msgid")
	}
	if res.File != nil {
		t.Error("File should be nil for text send")
	}

	// Verify WA was called on the server side.
	if len(fw.sendTextCalls) != 1 {
		t.Fatalf("expected 1 SendText call, got %d", len(fw.sendTextCalls))
	}
	if fw.sendTextCalls[0].Text != "hello via IPC" {
		t.Errorf("Text = %q, want %q", fw.sendTextCalls[0].Text, "hello via IPC")
	}

	// Verify DB persistence.
	msgs, err := a.db.ListMessages(store.ListMessagesParams{
		ChatJID: wantJID,
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if !msgs[0].FromMe {
		t.Error("expected FromMe=true")
	}
}

func TestDelegateSendFile_RoundTrip(t *testing.T) {
	a := newIPCTestApp(t)
	fw := newFakeWA()
	a.wa = fw

	srv, err := a.startSendDelegateServer()
	if err != nil {
		t.Fatalf("startSendDelegateServer: %v", err)
	}
	defer srv.Close()

	// Write a test file.
	tmpFile := filepath.Join(a.opts.StoreDir, "test.png")
	if err := os.WriteFile(tmpFile, []byte("fake png"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	res, err := DelegateSendFile(context.Background(), a.opts.StoreDir, SendFileParams{
		To:       "6591234567",
		FilePath: tmpFile,
		Caption:  "test image",
	})
	if err != nil {
		t.Fatalf("DelegateSendFile: %v", err)
	}

	if res.File == nil {
		t.Fatal("File should not be nil")
	}
	if res.File.Name != "test.png" {
		t.Errorf("Name = %q, want %q", res.File.Name, "test.png")
	}
	if res.File.Media != "image" {
		t.Errorf("Media = %q, want %q", res.File.Media, "image")
	}

	// Verify upload and proto send were recorded.
	if len(fw.uploadCalls) != 1 {
		t.Errorf("expected 1 Upload call, got %d", len(fw.uploadCalls))
	}
	if len(fw.sendProtoCalls) != 1 {
		t.Errorf("expected 1 SendProtoMessage call, got %d", len(fw.sendProtoCalls))
	}
}

func TestDelegateSend_MissingSocket(t *testing.T) {
	dir := ipcTestDir(t)

	_, err := DelegateSendText(context.Background(), dir, SendTextParams{
		To:      "6591234567",
		Message: "hello",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrSendDelegateUnavailable) {
		t.Errorf("expected ErrSendDelegateUnavailable, got: %v", err)
	}
}

func TestDelegateSend_RefusedSocket(t *testing.T) {
	dir := ipcTestDir(t)
	sockPath := SendSocketPath(dir)

	// Create a listener and close it, leaving the stale socket file.
	addr := &net.UnixAddr{Name: sockPath, Net: "unix"}
	ln, err := net.ListenUnix("unix", addr)
	if err != nil {
		t.Fatalf("ListenUnix: %v", err)
	}
	_ = ln.Close()
	// Socket file should remain.

	_, err = DelegateSendText(context.Background(), dir, SendTextParams{
		To:      "6591234567",
		Message: "hello",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrSendDelegateUnavailable) {
		t.Errorf("expected ErrSendDelegateUnavailable, got: %v", err)
	}
}

func TestDelegateSend_RemoteError(t *testing.T) {
	a := newIPCTestApp(t)
	fw := newFakeWA()
	fw.sendTextErr = fmt.Errorf("not authenticated; run `wacli auth`")
	a.wa = fw

	srv, err := a.startSendDelegateServer()
	if err != nil {
		t.Fatalf("startSendDelegateServer: %v", err)
	}
	defer srv.Close()

	_, err = DelegateSendText(context.Background(), a.opts.StoreDir, SendTextParams{
		To:      "6591234567",
		Message: "hello",
	})
	if err == nil {
		t.Fatal("expected error")
	}

	// Should be a remote error, not unavailable.
	var re *SendDelegateRemoteError
	if !errors.As(err, &re) {
		t.Fatalf("expected SendDelegateRemoteError, got %T: %v", err, err)
	}
	if errors.Is(err, ErrSendDelegateUnavailable) {
		t.Error("remote error should not be ErrSendDelegateUnavailable")
	}
}

func TestDelegateSend_ProtocolVersionMismatch(t *testing.T) {
	dir := ipcTestDir(t)
	sockPath := SendSocketPath(dir)

	// Start a custom listener that returns a response with wrong version.
	addr := &net.UnixAddr{Name: sockPath, Net: "unix"}
	ln, err := net.ListenUnix("unix", addr)
	if err != nil {
		t.Fatalf("ListenUnix: %v", err)
	}
	defer ln.Close()
	defer os.Remove(sockPath)

	go func() {
		conn, err := ln.AcceptUnix()
		if err != nil {
			return
		}
		defer conn.Close()
		// Consume request.
		var raw json.RawMessage
		_ = json.NewDecoder(conn).Decode(&raw)
		// Return response with wrong version.
		_ = json.NewEncoder(conn).Encode(sendDelegateResponse{
			Version: 99,
			Result:  &SendResult{To: "jid", ID: "id"},
		})
	}()

	_, err = DelegateSendText(context.Background(), dir, SendTextParams{
		To:      "6591234567",
		Message: "hello",
	})
	if err == nil {
		t.Fatal("expected error")
	}

	var pe *SendDelegateProtocolError
	if !errors.As(err, &pe) {
		t.Fatalf("expected SendDelegateProtocolError, got %T: %v", err, err)
	}
	if pe.ExpectedVersion != 1 || pe.ActualVersion != 99 {
		t.Errorf("version = %d/%d, want 1/99", pe.ExpectedVersion, pe.ActualVersion)
	}
	if errors.Is(err, ErrSendDelegateUnavailable) {
		t.Error("protocol error should not be ErrSendDelegateUnavailable")
	}
}

func TestStartSendDelegateServer_StaleSocketCleanup(t *testing.T) {
	a := newIPCTestApp(t)
	fw := newFakeWA()
	a.wa = fw
	sockPath := SendSocketPath(a.opts.StoreDir)

	// Create a stale socket.
	addr := &net.UnixAddr{Name: sockPath, Net: "unix"}
	staleLn, err := net.ListenUnix("unix", addr)
	if err != nil {
		t.Fatalf("create stale socket: %v", err)
	}
	staleLn.SetUnlinkOnClose(false)
	_ = staleLn.Close()

	// Verify stale socket exists.
	if _, err := os.Lstat(sockPath); err != nil {
		t.Fatalf("stale socket should exist: %v", err)
	}

	// Starting the server should clean up the stale socket and succeed.
	srv, err := a.startSendDelegateServer()
	if err != nil {
		t.Fatalf("startSendDelegateServer: %v", err)
	}
	defer srv.Close()

	// Socket should exist (new one).
	if _, err := os.Lstat(sockPath); err != nil {
		t.Errorf("socket should exist after server start: %v", err)
	}
}

func TestSendDelegateServer_SocketRemovedOnClose(t *testing.T) {
	a := newIPCTestApp(t)
	fw := newFakeWA()
	a.wa = fw
	sockPath := SendSocketPath(a.opts.StoreDir)

	srv, err := a.startSendDelegateServer()
	if err != nil {
		t.Fatalf("startSendDelegateServer: %v", err)
	}

	// Socket should exist while server is running.
	if _, err := os.Lstat(sockPath); err != nil {
		t.Fatalf("socket should exist while server runs: %v", err)
	}

	_ = srv.Close()

	// Socket should be gone after close.
	if _, err := os.Lstat(sockPath); !os.IsNotExist(err) {
		t.Errorf("socket should be removed after close, got err: %v", err)
	}
}

func TestDelegateSend_TimeoutEncoding(t *testing.T) {
	dir := ipcTestDir(t)
	sockPath := SendSocketPath(dir)

	// Start a custom listener that captures the request.
	addr := &net.UnixAddr{Name: sockPath, Net: "unix"}
	ln, err := net.ListenUnix("unix", addr)
	if err != nil {
		t.Fatalf("ListenUnix: %v", err)
	}
	defer ln.Close()
	defer os.Remove(sockPath)

	reqCh := make(chan sendDelegateRequest, 1)
	go func() {
		conn, err := ln.AcceptUnix()
		if err != nil {
			return
		}
		defer conn.Close()
		var req sendDelegateRequest
		_ = json.NewDecoder(conn).Decode(&req)
		reqCh <- req
		_ = json.NewEncoder(conn).Encode(sendDelegateResponse{
			Version: sendDelegateProtocolVersion,
			Result:  &SendResult{To: "jid", ID: "id"},
		})
	}()

	// Send with a 2-second deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err = DelegateSendText(ctx, dir, SendTextParams{To: "123", Message: "hi"})
	if err != nil {
		t.Fatalf("DelegateSendText: %v", err)
	}

	select {
	case req := <-reqCh:
		if req.TimeoutMS <= 0 {
			t.Errorf("TimeoutMS = %d, want > 0", req.TimeoutMS)
		}
		if req.TimeoutMS > 2000 {
			t.Errorf("TimeoutMS = %d, want <= 2000", req.TimeoutMS)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for request")
	}
}

func TestDelegateSend_NoTimeoutWithoutDeadline(t *testing.T) {
	dir := ipcTestDir(t)
	sockPath := SendSocketPath(dir)

	addr := &net.UnixAddr{Name: sockPath, Net: "unix"}
	ln, err := net.ListenUnix("unix", addr)
	if err != nil {
		t.Fatalf("ListenUnix: %v", err)
	}
	defer ln.Close()
	defer os.Remove(sockPath)

	reqCh := make(chan sendDelegateRequest, 1)
	go func() {
		conn, err := ln.AcceptUnix()
		if err != nil {
			return
		}
		defer conn.Close()
		var req sendDelegateRequest
		_ = json.NewDecoder(conn).Decode(&req)
		reqCh <- req
		_ = json.NewEncoder(conn).Encode(sendDelegateResponse{
			Version: sendDelegateProtocolVersion,
			Result:  &SendResult{To: "jid", ID: "id"},
		})
	}()

	// Send WITHOUT a deadline.
	_, err = DelegateSendText(context.Background(), dir, SendTextParams{To: "123", Message: "hi"})
	if err != nil {
		t.Fatalf("DelegateSendText: %v", err)
	}

	select {
	case req := <-reqCh:
		if req.TimeoutMS != 0 {
			t.Errorf("TimeoutMS = %d, want 0 (no deadline)", req.TimeoutMS)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for request")
	}
}

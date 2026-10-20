package delegate

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestRoundTripAndRestart(t *testing.T) {
	dir, err := os.MkdirTemp("", "wacli-ipc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if runtime.GOOS == "windows" {
		// An inaccessible legacy endpoint must not prevent startup or restart.
		if err := os.Mkdir(filepath.Join(dir, ".send.sock"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		ln, err := Listen(dir)
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			conn, err := ln.Accept()
			if err == nil {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				_, err = conn.Write([]byte("reply"))
			}
			done <- err
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		conn, err := Dial(ctx, dir)
		cancel()
		if err != nil {
			_ = ln.Close()
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		got, err := io.ReadAll(conn)
		_ = conn.Close()
		_ = ln.Close()
		if err != nil || string(got) != "reply" {
			t.Fatalf("reply = %q, err = %v", got, err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestDialCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	conn, err := Dial(ctx, t.TempDir())
	if conn != nil {
		_ = conn.Close()
	}
	if err == nil {
		t.Fatal("dial unexpectedly succeeded")
	}
}

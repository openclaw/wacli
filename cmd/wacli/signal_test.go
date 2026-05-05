package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/steipete/wacli/internal/out"
)

func TestSignalContextWithEventsKeepsStderrNDJSON(t *testing.T) {
	var stderr bytes.Buffer
	exits := make(chan int, 1)
	sigCh := make(chan os.Signal, 2)
	ctx, stop := signalContextForChannel(out.NewEventWriter(&stderr, true), sigCh, nil, func(code int) {
		exits <- code
	})
	defer stop()

	sigCh <- os.Interrupt
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("context was not canceled after first signal")
	}

	sigCh <- syscall.SIGTERM
	select {
	case code := <-exits:
		if code != 1 {
			t.Fatalf("exit code = %d, want 1", code)
		}
	case <-time.After(time.Second):
		t.Fatal("force-exit callback was not called after second signal")
	}

	raw := stderr.String()
	if strings.Contains(raw, "Shutting down") || strings.Contains(raw, "Force quit") {
		t.Fatalf("human signal text leaked into --events stderr:\n%s", raw)
	}

	var sawShutdown, sawForceQuit bool
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		var evt struct {
			Event string         `json:"event"`
			Data  map[string]any `json:"data"`
		}
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			t.Fatalf("signal line is not JSON %q: %v", line, err)
		}
		if evt.Event != "signal" {
			t.Fatalf("event = %q, want signal", evt.Event)
		}
		switch evt.Data["action"] {
		case "shutdown":
			sawShutdown = true
		case "force_quit":
			sawForceQuit = true
		}
	}
	if !sawShutdown || !sawForceQuit {
		t.Fatalf("missing signal events shutdown=%v force_quit=%v in:\n%s", sawShutdown, sawForceQuit, raw)
	}

	if err := ctx.Err(); err != context.Canceled {
		t.Fatalf("ctx.Err() = %v, want context.Canceled", err)
	}
}

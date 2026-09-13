package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/openclaw/wacli/internal/out"
	signalLog "go.mau.fi/libsignal/logger"
	"go.mau.fi/libsignal/protocol"
	"go.mau.fi/libsignal/serialize"
	"go.mau.fi/libsignal/session"
	"go.mau.fi/libsignal/signalerror"
	"go.mau.fi/libsignal/state/record"
)

func TestLibsignalOutputHelper(t *testing.T) {
	mode := os.Getenv("WACLI_TEST_LIBSIGNAL_OUTPUT")
	if mode == "" {
		t.Skip("subprocess helper")
	}
	if mode != "startup" {
		if mode == "reset" {
			os.Args = []string{"wacli", "version", "--events"}
			main()
		}
		os.Args = []string{"wacli", "version"}
		if mode == "events" {
			os.Args = append(os.Args, "--events")
		}
		main()
	}

	// Exercise the installed global logger, not a separately constructed adapter.
	signalLog.Configure("all")
	signalLog.Debug("Using cipherKey: SYNTHETIC-KEY-DO-NOT-LOG")
	signalLog.Info("SYNTHETIC-INFO-DO-NOT-LOG")
	serializer := serialize.NewProtoBufSerializer()
	_, err := protocol.NewSignalMessageFromBytes([]byte("RAW\nKEY"), serializer.SignalMessage)
	if err == nil {
		t.Fatal("short synthetic signal message unexpectedly parsed")
	}
	// An empty in-memory record fails before any key, store, or network access.
	var cipher session.Cipher
	_, _, err = cipher.DecryptWithRecord(context.Background(), record.NewSession(serializer.Session, serializer.State), nil)
	if !errors.Is(err, signalerror.ErrNoValidSessions) {
		t.Fatalf("empty session error = %v", err)
	}
	signalLog.Warning("uninitialized session: SYNTHETIC-WARN\n\x1b[31m")
	if mode == "events" {
		if err := out.NewEventWriter(os.Stderr, true).Emit("progress", map[string]any{"messages_synced": 0}); err != nil {
			t.Fatal(err)
		}
	}
	if mode == "startup" {
		if err := out.WriteJSON(os.Stdout, map[string]any{"synthetic": true}); err != nil {
			t.Fatal(err)
		}
	}
	os.Exit(0)
}

func TestLibsignalOutput(t *testing.T) {
	for _, mode := range []string{"startup", "human", "events", "reset"} {
		t.Run(mode, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestLibsignalOutputHelper$", "-test.parallel=1")
			cmd.Env = append(os.Environ(), "WACLI_TEST_LIBSIGNAL_OUTPUT="+mode)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("helper: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
			}
			wantStdout := effectiveVersion() + "\n"
			if mode == "startup" {
				wantStdout = "{\"success\":true,\"data\":{\"synthetic\":true},\"error\":null}\n"
			} else if mode == "reset" {
				wantStdout += wantStdout
			}
			if stdout.String() != wantStdout {
				t.Errorf("stdout contaminated: got %q, want %q", stdout.String(), wantStdout)
			}
			raw := stderr.String()
			for _, secret := range []string{"RAW", "KEY", "SYNTHETIC-INFO", "SYNTHETIC-WARN", "\x1b"} {
				if strings.Contains(raw, secret) {
					t.Errorf("diagnostics leaked %q: %q", secret, raw)
				}
			}
			wantMessages := []string{
				"Error split signal message (details redacted)",
				"Unable to decrypt message with state (details redacted)",
				"uninitialized session",
				"Signal diagnostic (details redacted)",
			}
			wantLevels := []string{"error", "error", "warning", "warning"}
			lines := strings.Split(strings.TrimSpace(raw), "\n")
			wantLines := len(wantMessages)
			if mode == "events" {
				wantLines++
			}
			if len(lines) != wantLines {
				t.Fatalf("stderr lines = %d, want %d: %q", len(lines), wantLines, raw)
			}
			for i, line := range lines {
				if mode != "events" {
					if !strings.HasPrefix(line, "[libsignal "+wantLevels[i]+"] ") || !strings.HasSuffix(line, ": "+wantMessages[i]) {
						t.Errorf("unexpected human diagnostic: %q", line)
					}
					continue
				}
				var event struct {
					Event string         `json:"event"`
					TS    int64          `json:"ts"`
					Data  map[string]any `json:"data"`
				}
				if err := json.Unmarshal([]byte(line), &event); err != nil || event.TS == 0 {
					t.Fatalf("invalid NDJSON event: %q (%v)", line, err)
				}
				if i == len(wantMessages) {
					if event.Event != "progress" || event.Data["messages_synced"] != float64(0) {
						t.Errorf("progress event changed: %q", line)
					}
					continue
				}
				caller, _ := event.Data["caller"].(string)
				if event.Event != "warning" || event.Data["code"] != "libsignal_diagnostic" ||
					event.Data["level"] != wantLevels[i] || event.Data["source"] != "libsignal" ||
					event.Data["message"] != wantMessages[i] || caller == "" {
					t.Errorf("unexpected libsignal event: %q", line)
				}
			}
		})
	}
}

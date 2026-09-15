package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/types"
)

func TestWriteMarkReadOutputReadSelfHint(t *testing.T) {
	t.Run("plain output prints a note", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		flags := &rootFlags{}
		if err := writeMarkReadOutput(flags, &stdout, &stderr, "123@s.whatsapp.net", []string{"A"}, types.ReceiptTypeReadSelf); err != nil {
			t.Fatalf("writeMarkReadOutput: %v", err)
		}
		if !strings.HasPrefix(stdout.String(), "Sent read-self receipt(s) for 1 message(s)") {
			t.Fatalf("stdout = %q", stdout.String())
		}
		if !strings.HasPrefix(stderr.String(), "Note: ") {
			t.Fatalf("stderr = %q, want a plain note", stderr.String())
		}
	})
	t.Run("events output emits an NDJSON warning", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		flags := &rootFlags{events: true}
		if err := writeMarkReadOutput(flags, &stdout, &stderr, "123@s.whatsapp.net", []string{"A"}, types.ReceiptTypeReadSelf); err != nil {
			t.Fatalf("writeMarkReadOutput: %v", err)
		}
		lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
		if len(lines) != 1 {
			t.Fatalf("stderr = %q, want exactly one event line", stderr.String())
		}
		var evt struct {
			Event string         `json:"event"`
			Data  map[string]any `json:"data"`
		}
		if err := json.Unmarshal([]byte(lines[0]), &evt); err != nil {
			t.Fatalf("stderr is not NDJSON: %v: %q", err, lines[0])
		}
		if evt.Event != "warning" || evt.Data["code"] != "read_self_receipt" || evt.Data["receipt"] != "read-self" {
			t.Fatalf("event = %+v", evt)
		}
	})
	t.Run("json output carries the receipt and no plain note", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		flags := &rootFlags{asJSON: true}
		if err := writeMarkReadOutput(flags, &stdout, &stderr, "123@s.whatsapp.net", []string{"A"}, types.ReceiptTypeReadSelf); err != nil {
			t.Fatalf("writeMarkReadOutput: %v", err)
		}
		if stderr.Len() != 0 {
			t.Fatalf("stderr = %q, want nothing under --json", stderr.String())
		}
		var res struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
			t.Fatalf("stdout is not JSON: %v", err)
		}
		if res.Data["receipt"] != "read-self" || res.Data["sender_notified"] != false {
			t.Fatalf("data = %+v", res.Data)
		}
	})
	t.Run("read receipt prints the marked line only", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if err := writeMarkReadOutput(&rootFlags{}, &stdout, &stderr, "123@s.whatsapp.net", []string{"A", "B"}, types.ReceiptTypeRead); err != nil {
			t.Fatalf("writeMarkReadOutput: %v", err)
		}
		if stdout.String() != "Marked 2 message(s) as read in 123@s.whatsapp.net\n" || stderr.Len() != 0 {
			t.Fatalf("stdout = %q, stderr = %q", stdout.String(), stderr.String())
		}
	})
}

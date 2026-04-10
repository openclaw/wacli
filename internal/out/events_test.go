package out

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestEventWriterDisabledIsNoOp(t *testing.T) {
	var b bytes.Buffer
	w := NewEventWriter(&b, false)
	if err := w.Emit("connected", nil); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if b.Len() != 0 {
		t.Fatalf("expected no output when disabled, got %q", b.String())
	}
}

func TestEventWriterEmitsNDJSON(t *testing.T) {
	var b bytes.Buffer
	w := NewEventWriter(&b, true)
	w.clockNow = func() time.Time { return time.UnixMilli(1234).UTC() }

	if err := w.Emit("progress", map[string]any{"messages_synced": 25}); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	line := strings.TrimSpace(b.String())
	var got map[string]any
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["event"] != "progress" {
		t.Fatalf("unexpected event: %v", got["event"])
	}
	if got["ts"] != float64(1234) {
		t.Fatalf("unexpected ts: %v", got["ts"])
	}
	data, ok := got["data"].(map[string]any)
	if !ok {
		t.Fatalf("expected data object, got %T", got["data"])
	}
	if data["messages_synced"] != float64(25) {
		t.Fatalf("unexpected data payload: %#v", data)
	}
}

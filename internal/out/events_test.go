package out

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestEventWriterEmitsNDJSON(t *testing.T) {
	var buf bytes.Buffer
	ew := NewEventWriter(&buf, true)

	if !ew.Enabled() {
		t.Fatal("expected Enabled() to be true")
	}

	ew.Emit("connected", nil)
	ew.Emit("progress", map[string]interface{}{"messages_synced": 75})
	ew.Emit("history_sync", map[string]interface{}{"conversations": 42})

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d: %q", len(lines), buf.String())
	}

	// Verify first event: connected
	var ev1 Event
	if err := json.Unmarshal([]byte(lines[0]), &ev1); err != nil {
		t.Fatalf("unmarshal line 0: %v", err)
	}
	if ev1.Event != "connected" {
		t.Fatalf("expected event=connected, got %q", ev1.Event)
	}
	if ev1.TS <= 0 {
		t.Fatal("expected positive timestamp")
	}

	// Verify second event: progress with data
	var ev2 struct {
		Event string                 `json:"event"`
		Data  map[string]interface{} `json:"data"`
		TS    int64                  `json:"ts"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &ev2); err != nil {
		t.Fatalf("unmarshal line 1: %v", err)
	}
	if ev2.Event != "progress" {
		t.Fatalf("expected event=progress, got %q", ev2.Event)
	}
	if ev2.Data["messages_synced"] != float64(75) {
		t.Fatalf("expected messages_synced=75, got %v", ev2.Data["messages_synced"])
	}

	// Verify third event: history_sync
	var ev3 struct {
		Event string                 `json:"event"`
		Data  map[string]interface{} `json:"data"`
		TS    int64                  `json:"ts"`
	}
	if err := json.Unmarshal([]byte(lines[2]), &ev3); err != nil {
		t.Fatalf("unmarshal line 2: %v", err)
	}
	if ev3.Event != "history_sync" {
		t.Fatalf("expected event=history_sync, got %q", ev3.Event)
	}
	if ev3.Data["conversations"] != float64(42) {
		t.Fatalf("expected conversations=42, got %v", ev3.Data["conversations"])
	}
}

func TestEventWriterDisabledIsNoop(t *testing.T) {
	var buf bytes.Buffer
	ew := NewEventWriter(&buf, false)

	if ew.Enabled() {
		t.Fatal("expected Enabled() to be false")
	}

	ew.Emit("connected", nil)
	ew.Emit("progress", map[string]interface{}{"messages_synced": 10})

	if buf.Len() != 0 {
		t.Fatalf("expected no output when disabled, got %q", buf.String())
	}
}

func TestEventWriterNilData(t *testing.T) {
	var buf bytes.Buffer
	ew := NewEventWriter(&buf, true)

	ew.Emit("disconnected", nil)

	var ev map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ev["event"] != "disconnected" {
		t.Fatalf("expected event=disconnected, got %v", ev["event"])
	}
	// data should be absent (omitempty)
	if _, ok := ev["data"]; ok {
		t.Fatalf("expected data to be omitted for nil, got %v", ev["data"])
	}
}

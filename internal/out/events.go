package out

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

// Event is a single NDJSON line emitted to stderr when --events is set.
type Event struct {
	Event string      `json:"event"`
	Data  interface{} `json:"data,omitempty"`
	TS    int64       `json:"ts"`
}

// EventWriter emits structured NDJSON events to a writer.
// When disabled, it is a no-op; callers should check Enabled() and fall back to
// human-readable output.
type EventWriter struct {
	mu      sync.Mutex
	w       io.Writer
	enabled bool
}

// NewEventWriter returns an EventWriter. If enabled is false, Emit is a no-op.
func NewEventWriter(w io.Writer, enabled bool) *EventWriter {
	return &EventWriter{w: w, enabled: enabled}
}

// Enabled reports whether JSON events are active.
func (ew *EventWriter) Enabled() bool {
	return ew.enabled
}

// Emit writes a single NDJSON event line. Safe for concurrent use.
func (ew *EventWriter) Emit(event string, data interface{}) {
	if !ew.enabled {
		return
	}
	e := Event{
		Event: event,
		Data:  data,
		TS:    time.Now().UnixMilli(),
	}
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	ew.mu.Lock()
	defer ew.mu.Unlock()
	fmt.Fprintln(ew.w, string(b))
}

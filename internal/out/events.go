package out

import (
	"encoding/json"
	"io"
	"sync"
	"time"
)

type EventWriter struct {
	mu       sync.Mutex
	w        io.Writer
	enabled  bool
	clockNow func() time.Time
}

type eventEnvelope struct {
	Event string         `json:"event"`
	Data  map[string]any `json:"data,omitempty"`
	TS    int64          `json:"ts"`
}

func NewEventWriter(w io.Writer, enabled bool) *EventWriter {
	return &EventWriter{
		w:        w,
		enabled:  enabled,
		clockNow: time.Now,
	}
}

func (e *EventWriter) Enabled() bool {
	return e != nil && e.enabled && e.w != nil
}

func (e *EventWriter) Emit(event string, data map[string]any) error {
	if !e.Enabled() {
		return nil
	}

	payload := eventEnvelope{
		Event: event,
		Data:  data,
		TS:    e.clockNow().UTC().UnixMilli(),
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	_, err = e.w.Write(append(b, '\n'))
	return err
}

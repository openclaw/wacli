// Package rpc implements the JSON-RPC 2.0 server and the event hub used to
// push real-time notifications to subscribed clients.
//
// Event types pushed via the "event" notification:
//
//	message.received  – an incoming or outgoing message was received
//	  payload: {id, chatJid, senderJid, fromMe, text, timestamp,
//	            pushName?, reactionEmoji?, reactionToId?,
//	            media?:{type, mimeType, caption, directPath, fileLength}}
//
//	message.sent      – a read receipt was received confirming delivery
//	  payload: {ids, chatJid}
//
//	typing            – a chat presence (typing) notification
//	  payload: {chatJid, senderJid, state ("composing"|"paused"), media}
//
//	presence          – an online/offline presence update
//	  payload: {from, status ("available"|"unavailable"), lastSeen?}
package rpc

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

// Event is a server-initiated notification sent to subscribed clients.
type Event struct {
	Type      string         `json:"type"`
	Timestamp string         `json:"timestamp"`
	Payload   map[string]any `json:"payload"`
}

type subscriber struct {
	id string
	ch chan Event
}

// Hub is the internal event bus that bridges whatsmeow events to RPC clients.
type Hub struct {
	mu         sync.RWMutex
	subs       map[string]*subscriber
	bufferSize int
	closed     bool
}

// NewHub creates a new Hub with the given per-subscriber channel buffer size.
func NewHub(bufferSize int) *Hub {
	if bufferSize <= 0 {
		bufferSize = 256
	}
	return &Hub{
		subs:       make(map[string]*subscriber),
		bufferSize: bufferSize,
	}
}

// Subscribe registers a new subscriber and returns its ID, a read-only event
// channel, and a cancel function to remove the subscription.
func (h *Hub) Subscribe() (id string, ch <-chan Event, cancel func()) {
	h.mu.Lock()
	defer h.mu.Unlock()

	id = uuid.New().String()
	evCh := make(chan Event, h.bufferSize)
	sub := &subscriber{id: id, ch: evCh}
	h.subs[id] = sub

	cancel = func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if s, ok := h.subs[id]; ok {
			close(s.ch)
			delete(h.subs, id)
		}
	}
	return id, evCh, cancel
}

// Publish broadcasts an event to all subscribers.  It is non-blocking: if a
// subscriber's buffer is full the event is dropped for that subscriber only.
func (h *Hub) Publish(evt Event) {
	if evt.Timestamp == "" {
		evt.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, sub := range h.subs {
		select {
		case sub.ch <- evt:
		default:
			// Slow subscriber – drop the event rather than block WA handler.
		}
	}
}

// Close closes all subscriber channels and removes them.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for id, sub := range h.subs {
		close(sub.ch)
		delete(h.subs, id)
	}
}

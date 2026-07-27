package app

import (
	"fmt"
	"strings"
	"time"

	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// SyncWebhookEventKind identifies the selected webhook event kind. New event
// payloads carry it as "EventType"; message payloads deliberately omit it to
// preserve the established webhook body byte-for-byte.
type SyncWebhookEventKind string

const (
	SyncWebhookEventMessage      SyncWebhookEventKind = "message"
	SyncWebhookEventReceipt      SyncWebhookEventKind = "receipt"
	SyncWebhookEventChatPresence SyncWebhookEventKind = "chat_presence"
)

var syncWebhookEventKinds = []SyncWebhookEventKind{
	SyncWebhookEventMessage,
	SyncWebhookEventReceipt,
	SyncWebhookEventChatPresence,
}

// SyncWebhookEventSet is the set of event kinds the webhook forwards. The zero
// value forwards messages only.
type SyncWebhookEventSet map[SyncWebhookEventKind]bool

// ParseSyncWebhookEvents parses a comma-separated --webhook-events value. An
// empty value means the default: messages only.
func ParseSyncWebhookEvents(raw string) (SyncWebhookEventSet, error) {
	set := SyncWebhookEventSet{}
	for _, part := range strings.Split(raw, ",") {
		kind := SyncWebhookEventKind(strings.ToLower(strings.TrimSpace(part)))
		if kind == "" {
			continue
		}
		if !kind.valid() {
			return nil, fmt.Errorf("--webhook-events must be a comma-separated list of: %s (got %q)", strings.Join(syncWebhookEventNames(), ", "), strings.TrimSpace(part))
		}
		set[kind] = true
	}
	if len(set) == 0 {
		set[SyncWebhookEventMessage] = true
	}
	return set, nil
}

func (k SyncWebhookEventKind) valid() bool {
	for _, known := range syncWebhookEventKinds {
		if k == known {
			return true
		}
	}
	return false
}

func syncWebhookEventNames() []string {
	names := make([]string, 0, len(syncWebhookEventKinds))
	for _, kind := range syncWebhookEventKinds {
		names = append(names, string(kind))
	}
	return names
}

// Enabled reports whether the kind should be forwarded. A nil or empty set
// behaves like the default (messages only).
func (s SyncWebhookEventSet) Enabled(kind SyncWebhookEventKind) bool {
	if len(s) == 0 {
		return kind == SyncWebhookEventMessage
	}
	return s[kind]
}

// syncWebhookEvent is one queued webhook delivery. Exactly one of the payload
// fields is set, selected by Kind.
type syncWebhookEvent struct {
	Kind     SyncWebhookEventKind
	Message  wa.ParsedMessage
	Receipt  syncWebhookReceipt
	Presence syncWebhookChatPresence
}

// id is only used for log lines; receipts carry a batch of message IDs and
// presence carries none, so it identifies the chat instead.
func (e syncWebhookEvent) id() string {
	switch e.Kind {
	case SyncWebhookEventReceipt:
		return strings.Join(e.Receipt.MessageIDs, ",")
	case SyncWebhookEventChatPresence:
		return e.Presence.Chat.String()
	default:
		return e.Message.ID
	}
}

// logFields identifies the event in a warning. message_id is only set for
// messages: the other kinds have no single message ID, and publishing a chat
// JID under that key would mislead machine consumers of the NDJSON events.
func (e syncWebhookEvent) logFields() map[string]any {
	fields := map[string]any{"event_type": string(e.Kind), "event_id": e.id()}
	if e.Kind == SyncWebhookEventMessage {
		fields["message_id"] = e.Message.ID
	}
	return fields
}

type syncWebhookReceipt struct {
	Chat       types.JID `json:"Chat"`
	Sender     types.JID `json:"Sender"`
	MessageIDs []string  `json:"MessageIDs"`
	Timestamp  time.Time `json:"Timestamp"`
	Type       string    `json:"Type"`
	IsFromMe   bool      `json:"IsFromMe"`
}

type syncWebhookChatPresence struct {
	Chat   types.JID `json:"Chat"`
	Sender types.JID `json:"Sender"`
	State  string    `json:"State"`
	Media  string    `json:"Media"`
}

// forwardedReceiptTypes are the only receipt types that cross the webhook. The
// rest (sender, retry, inactive, read-self, played-self, server-error,
// peer_msg, hist_sync) are protocol bookkeeping that would compete with real
// messages for the bounded queue, multiplied by participant in groups.
var forwardedReceiptTypes = map[types.ReceiptType]string{
	types.ReceiptTypeDelivered: "delivered",
	types.ReceiptTypeRead:      "read",
	types.ReceiptTypePlayed:    "played",
}

// newSyncWebhookReceiptEvent converts a receipt event into a webhook event,
// reporting false when the receipt type is not forwarded. The empty wire value
// for "delivered" is spelled out so consumers never have to switch on "".
func newSyncWebhookReceiptEvent(evt *events.Receipt) (syncWebhookEvent, bool) {
	if evt == nil || evt.Chat.IsEmpty() || len(evt.MessageIDs) == 0 {
		return syncWebhookEvent{}, false
	}
	receiptType, ok := forwardedReceiptTypes[evt.Type]
	if !ok {
		return syncWebhookEvent{}, false
	}
	ids := make([]string, 0, len(evt.MessageIDs))
	for _, id := range evt.MessageIDs {
		if strings.TrimSpace(id) == "" {
			continue
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return syncWebhookEvent{}, false
	}
	return syncWebhookEvent{
		Kind: SyncWebhookEventReceipt,
		Receipt: syncWebhookReceipt{
			Chat:       evt.Chat,
			Sender:     evt.Sender,
			MessageIDs: ids,
			Timestamp:  evt.Timestamp,
			Type:       receiptType,
			IsFromMe:   evt.IsFromMe,
		},
	}, true
}

func newSyncWebhookChatPresenceEvent(evt *events.ChatPresence) (syncWebhookEvent, bool) {
	if evt == nil || evt.Chat.IsEmpty() {
		return syncWebhookEvent{}, false
	}
	if evt.State != types.ChatPresenceComposing && evt.State != types.ChatPresencePaused {
		return syncWebhookEvent{}, false
	}
	return syncWebhookEvent{
		Kind: SyncWebhookEventChatPresence,
		Presence: syncWebhookChatPresence{
			Chat:   evt.Chat,
			Sender: evt.Sender,
			State:  string(evt.State),
			Media:  string(evt.Media),
		},
	}, true
}

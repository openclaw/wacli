package app

import (
	"context"
	"fmt"
	"strings"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// receiptStatus names the states a message of ours travels through, the ones a
// chat client shows as one tick, two ticks and blue ticks. WhatsApp spells
// "delivered" as the empty receipt type, so the mapping is explicit; everything
// else, such as sender or retry bookkeeping, says nothing about a recipient.
func receiptStatus(t types.ReceiptType) (string, bool) {
	switch t {
	case types.ReceiptTypeDelivered:
		return "delivered", true
	case types.ReceiptTypeRead:
		return "read", true
	case types.ReceiptTypePlayed:
		return "played", true
	}
	return "", false
}

// chatWithOurself reports whether a chat is the account's own notes chat, the
// one case where a receipt from this account is a delivery report rather than a
// record of what we read elsewhere.
func (a *App) chatWithOurself(chat types.JID) bool {
	if chat.IsEmpty() || a.wa == nil {
		return false
	}
	for _, own := range []string{a.wa.LinkedJID(), a.wa.LinkedLID()} {
		parsed, err := types.ParseJID(strings.TrimSpace(own))
		if err != nil || parsed.IsEmpty() {
			continue
		}
		if chat.User == parsed.User {
			return true
		}
	}
	return false
}

// handleOutgoingReceiptEvent records how far a message of ours has reached,
// one row per recipient, so a group where one member has read is not mistaken
// for one where everybody has.
func (a *App) handleOutgoingReceiptEvent(ctx context.Context, evt *events.Receipt) {
	if evt == nil || evt.Chat.IsEmpty() || len(evt.MessageIDs) == 0 {
		return
	}
	// A receipt we sent ourselves usually reports what we read on another
	// device, which says nothing about our own messages. In our own chat it is
	// the only report there is: the recipient is another of our devices.
	if evt.IsFromMe && !a.chatWithOurself(evt.Chat) {
		return
	}
	status, ok := receiptStatus(evt.Type)
	if !ok {
		return
	}
	chat := canonicalJIDString(a.canonicalStoreJID(ctx, evt.Chat))
	recipient := evt.Sender
	if recipient.IsEmpty() {
		// A direct chat names no participant: the recipient is the chat itself.
		recipient = evt.Chat
	}
	recipientJID := canonicalJIDString(a.canonicalStoreJID(ctx, recipient))
	when := evt.Timestamp
	if when.IsZero() {
		when = nowUTC()
	}
	for _, id := range evt.MessageIDs {
		if err := a.db.UpsertMessageReceipt(chat, string(id), recipientJID, status, when); err != nil {
			a.emitWarning(
				"message_receipt_store_failed",
				fmt.Sprintf("warning: failed to store a %s receipt for chat %s: %v", status, chat, err),
				map[string]any{"chat_jid": chat, "status": status, "error": err.Error()},
			)
			return
		}
	}
}

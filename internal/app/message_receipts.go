package app

import (
	"context"
	"fmt"
	"strings"
	"unicode"

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
func (a *App) handleOutgoingReceiptEvent(ctx context.Context, evt *events.Receipt) error {
	if evt == nil {
		return nil
	}
	// A receipt we sent ourselves usually reports what we read on another
	// device, which says nothing about our own messages. In our own chat it is
	// the only report there is: the recipient is another of our devices.
	if evt.IsFromMe && !a.chatWithOurself(evt.Chat) {
		return nil
	}
	status, ok := receiptStatus(evt.Type)
	if !ok {
		return nil
	}
	invalid := func(reason string) error {
		err := fmt.Errorf("invalid outgoing receipt: %s", reason)
		a.emitWarning("message_receipt_invalid", "warning: "+err.Error()+"; skipped invalid receipt input and sync remains incomplete. Retry sync and report repeated invalid-receipt warnings.", map[string]any{"reason": reason})
		return err
	}
	if !validReceiptJID(evt.Chat) {
		return invalid("malformed chat JID")
	}
	if len(evt.MessageIDs) == 0 {
		return invalid("missing message IDs")
	}
	chatJID := a.canonicalStoreJID(ctx, evt.Chat)
	if !validReceiptJID(chatJID) {
		return invalid("malformed canonical chat JID")
	}
	chat := canonicalJIDString(chatJID)
	recipient := evt.Sender
	if recipient == types.EmptyJID && (evt.Chat.Server == types.DefaultUserServer || evt.Chat.Server == types.HiddenUserServer) {
		// A direct chat names no participant: the recipient is the chat itself.
		recipient = evt.Chat
	}
	if !validReceiptJID(recipient) {
		return invalid("malformed recipient JID")
	}
	recipient = a.canonicalStoreJID(ctx, recipient)
	if !validReceiptJID(recipient) {
		return invalid("malformed canonical recipient JID")
	}
	recipientJID := canonicalJIDString(recipient)
	when := evt.Timestamp
	if when.IsZero() {
		when = nowUTC()
	}
	for _, id := range evt.MessageIDs {
		if strings.TrimSpace(string(id)) == "" {
			a.warnUnattributedReceipts()
			continue
		}
		if err := a.db.UpsertMessageReceipt(chat, string(id), recipientJID, status, when); err != nil {
			a.emitWarning(
				"message_receipt_store_failed",
				fmt.Sprintf("warning: failed to store a %s receipt for chat %s: %v", status, chat, err),
				map[string]any{"chat_jid": chat, "status": status, "error": err.Error()},
			)
			return err
		}
	}
	return nil
}

// warnUnattributedReceipts reports, once per process, receipt entries that
// name no message. whatsmeow leaves the ID blank when a grouped group receipt
// arrives without a message key. Nothing can be stored for them and WhatsApp
// does not resend them, so retrying cannot help and they leave sync freshness
// alone; an active group can send thousands in one session.
func (a *App) warnUnattributedReceipts() {
	if !a.unattributedReceiptWarned.CompareAndSwap(false, true) {
		return
	}
	a.emitWarning(
		"message_receipt_unattributed",
		"warning: skipped delivery/read receipts that name no message; WhatsApp does not resend them, so those counts stay unchanged. Further occurrences in this sync are not reported.",
		nil,
	)
}

// Require both JID components before and after canonicalization. IsEmpty only
// checks the server, so it also accepts a server with no recipient user.
func validReceiptJID(jid types.JID) bool {
	for _, part := range []string{jid.User, jid.Server} {
		if part == "" || strings.IndexFunc(part, func(r rune) bool {
			return unicode.IsSpace(r) || r == '@' || r == '/'
		}) >= 0 {
			return false
		}
	}
	return true
}

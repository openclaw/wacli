package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"go.mau.fi/whatsmeow/types"

	"github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/wa"
)

// MarkMessagesRead sends WhatsApp read receipts for the given message IDs in
// chat. This is a receipt to the other party, unlike MarkChatRead, which is an
// app-state patch that only syncs the chat's read flag to this account's own
// devices.
//
// The returned receipt type is what WhatsApp actually sent: types.ReceiptTypeRead
// notifies the sender (blue ticks); types.ReceiptTypeReadSelf is what whatsmeow
// substitutes when this account's read-receipts privacy setting is off (or the
// chat is a newsletter), and it only syncs to this account's own devices.
//
// Receipts apply to received messages: any ID that the local store knows as one
// of this account's own outgoing messages is rejected, on every path. The store
// is checked under the chat's phone-number and LID forms, so an aliased row is
// not missed. IDs that are not in the store are accepted in direct chats and,
// with an explicit sender, in groups, so an unsynced received message can still
// be marked read; a store read failure is an error, never an allowance.
//
// In a group chat the receipt is addressed to the participant who sent the
// messages. When sender is empty, it is looked up from the local store; every
// ID must then be stored and belong to the same participant.
func (a *App) MarkMessagesRead(ctx context.Context, chat types.JID, ids []string, sender types.JID) (types.ReceiptType, error) {
	msgIDs := make([]types.MessageID, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		msgIDs = append(msgIDs, types.MessageID(id))
	}
	if len(msgIDs) == 0 {
		return "", fmt.Errorf("at least one message id is required")
	}
	resolveSender := chat.Server == types.GroupServer && sender.IsEmpty()
	chatKeys := a.receiptChatKeys(ctx, chat)
	var stored types.JID
	for _, id := range msgIDs {
		msg, found, err := storedMessageInChats(a.db, chatKeys, id)
		if err != nil {
			return "", fmt.Errorf("look up message %s in the local store: %w", id, err)
		}
		if !found {
			if resolveSender {
				return "", fmt.Errorf("message %s is not in the local store; pass --sender for group messages", id)
			}
			continue
		}
		if msg.FromMe {
			return "", fmt.Errorf("message %s was sent by you; read receipts apply to received messages", id)
		}
		if !resolveSender {
			continue
		}
		jid, err := wa.ParseUserOrJID(msg.SenderJID)
		if err != nil {
			return "", fmt.Errorf("message %s has no usable sender in the local store; pass --sender", id)
		}
		if !stored.IsEmpty() && stored.String() != jid.String() {
			return "", fmt.Errorf("messages %s and %s have different senders; mark them read separately", msgIDs[0], id)
		}
		stored = jid
	}
	if resolveSender {
		sender = stored
	}
	return a.wa.MarkRead(ctx, chat, sender, msgIDs)
}

// receiptChatKeys lists the chat JID strings a message in chat may be stored
// under: the chat itself plus, for a user chat, its LID or phone-number alias.
func (a *App) receiptChatKeys(ctx context.Context, chat types.JID) []string {
	keys := []types.JID{chat}
	switch chat.Server {
	case types.DefaultUserServer:
		keys = append(keys, a.wa.ResolvePNToLID(ctx, chat))
	case types.HiddenUserServer:
		keys = append(keys, a.wa.ResolveLIDToPN(ctx, chat))
	}
	out := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, jid := range keys {
		if jid.IsEmpty() {
			continue
		}
		if jid.Server == types.DefaultUserServer {
			jid = jid.ToNonAD()
		}
		s := jid.String()
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// storedMessageInChats returns the stored message with id under any of the
// chat keys. A missing row is reported as found=false; any other store error
// is returned as-is.
func storedMessageInChats(db *store.DB, chatKeys []string, id types.MessageID) (store.Message, bool, error) {
	for _, chatKey := range chatKeys {
		msg, err := db.GetMessage(chatKey, string(id))
		if err == nil {
			return msg, true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return store.Message{}, false, err
		}
	}
	return store.Message{}, false, nil
}

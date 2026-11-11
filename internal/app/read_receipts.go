package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow/types"
)

const maxReadReceiptMessages = 100

type readReceiptBatch struct {
	sender types.JID
	ids    []types.MessageID
}

// MarkChatReadWithReceipts uses the receipt transport independently of app-state
// recovery. Only a fully dispatched selection advances the local read boundary.
func (a *App) MarkChatReadWithReceipts(ctx context.Context, jid types.JID) (int, types.ReceiptType, error) {
	return a.markChatReadWithReceipts(ctx, jid, "")
}

// MarkChatReadWithReceiptsThrough keeps receipt privacy and batching unchanged,
// while acknowledging only unread messages at or before the exact inbound ID.
func (a *App) MarkChatReadWithReceiptsThrough(ctx context.Context, jid types.JID, messageID string) (int, types.ReceiptType, error) {
	if strings.TrimSpace(messageID) == "" {
		return 0, "", fmt.Errorf("--through-message-id requires an exact nonempty message ID")
	}
	return a.markChatReadWithReceipts(ctx, jid, messageID)
}

func (a *App) markChatReadWithReceipts(ctx context.Context, jid types.JID, throughID string) (int, types.ReceiptType, error) {
	chatJID := canonicalJIDString(a.canonicalStoreJID(ctx, jid))
	var msgs []store.MessageInfo
	var err error
	if throughID == "" {
		msgs, err = a.db.ReadReceiptCandidates(chatJID, maxReadReceiptMessages)
	} else {
		msgs, err = a.db.ReadReceiptCandidatesThrough(chatJID, throughID, maxReadReceiptMessages)
	}
	if err != nil {
		return 0, "", fmt.Errorf("select unread messages for %s: %w", chatJID, err)
	}
	if len(msgs) == 0 {
		if throughID != "" {
			// The unread suffix (or marker's latest message) can be entirely
			// beyond the handled anchor. Nothing was acknowledged in that case.
			return 0, "", nil
		}
		return 0, "", a.db.ClearChatUnreadMarker(chatJID)
	}
	sent, receipt, err := a.sendReadReceipts(ctx, jid, msgs)
	if err != nil {
		return sent, receipt, err
	}
	ids := make([]string, 0, len(msgs))
	for _, m := range msgs {
		ids = append(ids, m.MsgID)
	}
	clearUnread := a.db.ClearChatUnreadThrough
	if throughID != "" {
		// A marker-only arrival after selection must stay pending even when
		// its counter has not advanced by the time receipt transport finishes.
		clearUnread = a.db.ClearChatUnreadThroughPreservingMarker
	}
	if err := clearUnread(chatJID, msgs[len(msgs)-1].Timestamp, ids); err != nil {
		return sent, receipt, fmt.Errorf("receipts sent, but updating local unread state failed: %w", err)
	}
	return sent, receipt, nil
}

// sendReadReceipts dispatches receipts for exactly msgs, batched by sender, and
// reports how many were sent before any failure.
func (a *App) sendReadReceipts(ctx context.Context, jid types.JID, msgs []store.MessageInfo) (int, types.ReceiptType, error) {
	isGroup := jid.Server == types.GroupServer
	batches, err := readReceiptBatches(msgs, isGroup)
	if err != nil {
		return 0, "", err
	}
	var addressing types.AddressingMode
	if isGroup {
		info, err := a.wa.GetGroupInfo(ctx, jid)
		if err != nil {
			return 0, "", fmt.Errorf("load group addressing mode: %w", err)
		}
		if info == nil {
			return 0, "", fmt.Errorf("group addressing mode unavailable")
		}
		addressing = info.AddressingMode
	}
	sent := 0
	receipt := types.ReceiptTypeReadSelf
	for _, batch := range batches {
		kind, err := a.wa.MarkRead(ctx, batch.ids, nowUTC(), jid, batch.sender, addressing)
		if err != nil {
			return sent, receipt, fmt.Errorf("send read receipts to %s (%d already sent; retry may resend them): %w", jid, sent, err)
		}
		if kind != types.ReceiptTypeReadSelf {
			receipt = wa.ReadReceiptUnknown
		}
		sent += len(batch.ids)
	}
	return sent, receipt, nil
}

// MarkChatMessagesReadWithReceipts acknowledges exactly the given stored
// incoming messages, which must be the chat's oldest unread messages, so no
// unread message before them is skipped and nothing else is acknowledged. The
// local unread count is lowered only when nothing else changed the chat while
// the receipts were sent; settled reports whether it was.
func (a *App) MarkChatMessagesReadWithReceipts(ctx context.Context, jid types.JID, ids []string) (sent int, receipt types.ReceiptType, settled bool, err error) {
	if len(ids) == 0 || len(ids) > maxReadReceiptMessages {
		return 0, "", false, fmt.Errorf("--message-id takes 1 to %d message IDs", maxReadReceiptMessages)
	}
	chatJID := canonicalJIDString(a.canonicalStoreJID(ctx, jid))
	msgs, snap, err := func() ([]store.MessageInfo, store.ExactReceiptSnapshot, error) {
		a.unreadMu.Lock()
		defer a.unreadMu.Unlock()
		// Send nothing if the caller gave up while this waited.
		if err := ctx.Err(); err != nil {
			return nil, store.ExactReceiptSnapshot{}, err
		}
		return a.db.ReadReceiptCandidatesExact(chatJID, ids)
	}()
	if err != nil {
		return 0, "", false, fmt.Errorf("select messages to acknowledge in %s: %w", chatJID, err)
	}
	sent, receipt, err = a.sendReadReceipts(ctx, jid, msgs)
	if err != nil {
		return sent, receipt, false, err
	}
	settled, err = func() (bool, error) {
		a.unreadMu.Lock()
		defer a.unreadMu.Unlock()
		return a.db.SettleExactReceipts(chatJID, snap)
	}()
	if err != nil {
		return sent, receipt, false, fmt.Errorf("receipts sent, but updating local unread state failed: %w", err)
	}
	return sent, receipt, settled, nil
}

func readReceiptBatches(msgs []store.MessageInfo, isGroup bool) ([]readReceiptBatch, error) {
	var batches []readReceiptBatch
	index := make(map[string]int)
	for _, m := range msgs {
		if strings.TrimSpace(m.MsgID) == "" {
			return nil, fmt.Errorf("stored message has no ID")
		}
		var sender types.JID
		if isGroup {
			var err error
			sender, err = types.ParseJID(strings.TrimSpace(m.SenderJID))
			if err != nil || sender.IsEmpty() || (sender.Server != types.DefaultUserServer && sender.Server != types.HiddenUserServer) {
				return nil, fmt.Errorf("message %s has no valid group sender; sync its history before sending receipts", m.MsgID)
			}
		}
		key := sender.String()
		i, ok := index[key]
		if !ok {
			i = len(batches)
			index[key] = i
			batches = append(batches, readReceiptBatch{sender: sender})
		}
		batches[i].ids = append(batches[i].ids, types.MessageID(m.MsgID))
	}
	return batches, nil
}

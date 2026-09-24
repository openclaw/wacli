package wa

import (
	"context"
	"fmt"
	"time"

	"go.mau.fi/whatsmeow/types"
)

const ReadReceiptUnknown types.ReceiptType = "unknown"

type readReceiptTransport interface {
	TryFetchPrivacySettings(context.Context, bool) (*types.PrivacySettings, error)
	MarkRead(context.Context, []types.MessageID, time.Time, types.JID, types.JID, ...types.ReceiptType) error
}

func (c *Client) MarkRead(ctx context.Context, ids []types.MessageID, timestamp time.Time, chat, sender types.JID, addressing types.AddressingMode) (types.ReceiptType, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return "", fmt.Errorf("not connected")
	}
	if len(ids) == 0 {
		return "", fmt.Errorf("no message IDs specified")
	}
	info := rewriteMediaRetryInfoForLID(ctx, cli, types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: sender, IsGroup: chat.Server == types.GroupServer, AddressingMode: addressing}}, resolvePNToLID)
	return dispatchReadReceipt(ctx, cli, ids, timestamp, info.Chat, info.Sender)
}

func dispatchReadReceipt(ctx context.Context, cli readReceiptTransport, ids []types.MessageID, timestamp time.Time, chat, sender types.JID) (types.ReceiptType, error) {
	settings, err := cli.TryFetchPrivacySettings(ctx, false)
	if err != nil {
		return "", fmt.Errorf("read the account's read-receipt privacy setting: %w", err)
	}
	receipt := effectiveReadReceiptType(chat, *settings)
	if err := cli.MarkRead(ctx, ids, timestamp, chat, sender, receipt); err != nil {
		return "", err
	}
	// whatsmeow may downgrade read to read-self using a newer privacy snapshot.
	// Its API returns no emitted type; a later settings lookup cannot recover it.
	if receipt == types.ReceiptTypeRead {
		return ReadReceiptUnknown, nil
	}
	return receipt, nil
}

func effectiveReadReceiptType(chat types.JID, settings types.PrivacySettings) types.ReceiptType {
	if chat.Server == types.NewsletterServer || settings.ReadReceipts == types.PrivacySettingNone {
		return types.ReceiptTypeReadSelf
	}
	return types.ReceiptTypeRead
}

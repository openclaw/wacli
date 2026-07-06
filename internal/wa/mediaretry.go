package wa

import (
	"context"
	"errors"
	"fmt"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// MediaRetryResult mirrors waMmsRetry.MediaRetryNotification_ResultType.
const (
	MediaRetryGeneralError    = 0
	MediaRetrySuccess         = 1
	MediaRetryNotFound        = 2 // phone no longer has the media
	MediaRetryDecryptionError = 3
)

// SendMediaRetryReceipt asks the primary device (phone) to re-upload the media
// for a message whose direct download has expired. The phone answers with a
// MediaRetry event (delivered via the event handler) carrying a fresh location.
func (c *Client) SendMediaRetryReceipt(ctx context.Context, info *types.MessageInfo, mediaKey []byte) error {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return fmt.Errorf("not connected")
	}
	if info == nil {
		return fmt.Errorf("message info is required")
	}
	retryInfo := rewriteMediaRetryInfoForLID(ctx, cli, *info, c.resolvePNToLIDLocked)
	return cli.SendMediaRetryReceipt(ctx, &retryInfo, mediaKey)
}

func rewriteMediaRetryInfoForLID(ctx context.Context, cli *whatsmeow.Client, info types.MessageInfo, resolve pollVoteLIDResolver) types.MessageInfo {
	if cli == nil || cli.Store == nil || cli.Store.LIDMigrationTimestamp <= 0 || resolve == nil {
		return info
	}
	if info.Chat.Server == types.DefaultUserServer {
		info.Chat = resolve(ctx, cli, info.Chat)
		if info.Sender.Server == types.DefaultUserServer {
			info.Sender = resolve(ctx, cli, info.Sender)
		}
	} else if info.AddressingMode == types.AddressingModeLID && info.Sender.Server == types.DefaultUserServer {
		info.Sender = resolve(ctx, cli, info.Sender)
	}
	return info
}

// DecryptMediaRetry decrypts a MediaRetry notification with the message's media
// key. On success it returns the new direct path to download from; resultCode
// follows the MediaRetry* constants above (Success, NotFound, ...).
func DecryptMediaRetry(evt *events.MediaRetry, mediaKey []byte) (directPath string, resultCode int, err error) {
	notif, err := whatsmeow.DecryptMediaRetryNotification(evt, mediaKey)
	if err != nil {
		// The phone can report "gone" as an unencrypted error notification
		// rather than a decrypted NOT_FOUND result; normalize both to NotFound.
		if errors.Is(err, whatsmeow.ErrMediaNotAvailableOnPhone) {
			return "", MediaRetryNotFound, nil
		}
		return "", MediaRetryGeneralError, err
	}
	return notif.GetDirectPath(), int(notif.GetResult()), nil
}

package wa

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mdp/qrterminal/v3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

type Options struct {
	StorePath string
}

type Client struct {
	opts Options

	mu     sync.Mutex
	client *whatsmeow.Client
}

func New(opts Options) (*Client, error) {
	if strings.TrimSpace(opts.StorePath) == "" {
		return nil, fmt.Errorf("StorePath is required")
	}
	// Reject paths that could inject SQLite URI parameters (#177, mirror of #59).
	if strings.ContainsAny(opts.StorePath, "?#") {
		return nil, fmt.Errorf("StorePath must not contain '?' or '#'")
	}
	c := &Client{opts: opts}
	if err := c.init(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != nil {
		c.client.Disconnect()
	}
}

func (c *Client) IsAuthed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.client != nil && c.client.Store != nil && c.client.Store.ID != nil
}

func (c *Client) LinkedJID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client == nil || c.client.Store == nil || c.client.Store.ID == nil {
		return ""
	}
	return c.client.Store.ID.ToNonAD().String()
}

func (c *Client) LinkedLID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client == nil || c.client.Store == nil {
		return ""
	}
	lid := c.client.Store.GetLID()
	if lid.IsEmpty() {
		return ""
	}
	return lid.ToNonAD().String()
}

func (c *Client) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.client != nil && c.client.IsConnected()
}

func (c *Client) SetAutoReconnect(enabled bool) (bool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client == nil {
		return false, false
	}
	previous := c.client.EnableAutoReconnect
	if c.client.IsConnected() {
		return previous, false
	}
	c.client.EnableAutoReconnect = enabled
	return previous, true
}

type ConnectOptions struct {
	AllowQR         bool
	OnQRCode        func(code string)
	PairPhoneNumber string
	OnPairCode      func(code string)
	// SuppressInitialAvailablePresence skips the post-connect available
	// presence update for callers that need a quiet linked-device session.
	// The default false preserves normal WhatsApp linked-device behavior.
	SuppressInitialAvailablePresence bool
	// DetachSocket, when true, connects the websocket with a detached
	// context so it is not closed when the caller's context is cancelled.
	// This allows graceful shutdown to send a final PresenceUnavailable
	// stanza before the client is disconnected. Only sync uses this;
	// other commands keep the default (false) so --timeout and signal
	// cancellation still bound the connection.
	DetachSocket bool
}

func (c *Client) Connect(ctx context.Context, opts ConnectOptions) error {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil {
		return fmt.Errorf("whatsapp client is not initialized")
	}

	if cli.IsConnected() {
		return nil
	}

	authed := cli.Store != nil && cli.Store.ID != nil
	if !authed && !opts.AllowQR && opts.PairPhoneNumber == "" {
		return fmt.Errorf("not authenticated; run `wacli auth`")
	}

	var qrChan <-chan whatsmeow.QRChannelItem
	if !authed {
		ch, err := cli.GetQRChannel(ctx)
		if err != nil {
			return fmt.Errorf("get QR channel: %w", err)
		}
		qrChan = ch
	}

	// When DetachSocket is set (sync), connect with a detached context that
	// has a 30-second dial timeout. The dial is bounded so a hanging connect
	// cannot outlive SIGTERM indefinitely; after the dial succeeds the timer
	// is stopped so the websocket stays open until Disconnect()/Close().
	// Other commands keep the caller's context so --timeout and signal
	// cancellation still bound the connection.
	connCtx := ctx
	var dialCancel context.CancelFunc
	var dialTimer *time.Timer
	var dialStop func() bool
	if opts.DetachSocket {
		// Use a detached context so the websocket survives signal
		// cancellation, but also cancel the dial if the caller's
		// context fires (--max-reconnect, SIGTERM) so an in-flight
		// connect cannot outlive caller cancellation.
		connCtx, dialCancel = context.WithCancel(context.Background())
		dialTimer = time.AfterFunc(30*time.Second, dialCancel)
		dialStop = context.AfterFunc(ctx, dialCancel)
	}
	if err := cli.ConnectContext(connCtx); err != nil {
		if dialCancel != nil {
			dialCancel()
		}
		if dialTimer != nil {
			dialTimer.Stop()
		}
		if dialStop != nil {
			dialStop()
		}
		return err
	}
	// Dial succeeded — stop the timer and unregister the caller
	// cancellation hook so the websocket stays alive until Close().
	if dialTimer != nil {
		dialTimer.Stop()
	}
	if dialStop != nil {
		dialStop()
	}

	if authed {
		if opts.SuppressInitialAvailablePresence {
			return nil
		}
		sendInitialAvailablePresence(ctx, cli)
		return nil
	}

	// Wait for QR flow to succeed or fail.
	pairCodeRequested := false
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case evt, ok := <-qrChan:
			if !ok {
				return fmt.Errorf("QR channel closed")
			}
			switch {
			case evt.Event == whatsmeow.QRChannelEventCode:
				if opts.PairPhoneNumber != "" {
					if pairCodeRequested {
						continue
					}
					code, err := cli.PairPhone(ctx, opts.PairPhoneNumber, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
					if err != nil {
						return fmt.Errorf("pair phone: %w", err)
					}
					pairCodeRequested = true
					if opts.OnPairCode != nil {
						opts.OnPairCode(code)
					}
				} else if opts.OnQRCode != nil {
					opts.OnQRCode(evt.Code)
				} else {
					qrterminal.GenerateHalfBlock(evt.Code, qrterminal.M, os.Stdout)
				}
			case evt == whatsmeow.QRChannelSuccess:
				return nil
			default:
				if err := qrChannelEventError(evt); err != nil {
					return err
				}
			}
		}
	}
}

func qrChannelEventError(evt whatsmeow.QRChannelItem) error {
	switch {
	case evt == whatsmeow.QRChannelTimeout:
		return fmt.Errorf("QR code timed out; run `wacli auth` again to get a new code")
	case evt == whatsmeow.QRChannelClientOutdated:
		return fmt.Errorf("WhatsApp client outdated; update wacli and try again")
	case evt == whatsmeow.QRChannelScannedWithoutMultidevice:
		return fmt.Errorf("QR scanned, but multi-device is not enabled on the phone")
	case evt == whatsmeow.QRChannelErrUnexpectedEvent:
		return fmt.Errorf("unexpected QR pairing state; run `wacli auth` again")
	case evt.Event == whatsmeow.QRChannelEventPasskeyRequest:
		return fmt.Errorf("WhatsApp requires passkey verification, which wacli cannot safely complete yet; preserve any existing authenticated store and check for an updated wacli release")
	case evt.Event == whatsmeow.QRChannelEventPasskeyResponse:
		return fmt.Errorf("WhatsApp requires passkey confirmation, which wacli cannot safely complete yet; preserve any existing authenticated store and check for an updated wacli release")
	case evt.Event == whatsmeow.QRChannelEventError:
		if evt.Error != nil {
			return fmt.Errorf("QR pairing failed: %w", evt.Error)
		}
		return fmt.Errorf("QR pairing failed")
	default:
		return fmt.Errorf("unsupported QR pairing state %q; update wacli and try again", evt.Event)
	}
}

func (c *Client) AddEventHandler(handler func(any)) uint32 {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil {
		return 0
	}
	return cli.AddEventHandler(handler)
}

func (c *Client) RemoveEventHandler(id uint32) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil {
		return
	}
	cli.RemoveEventHandler(id)
}

func (c *Client) DecryptSecretEncryptedMessage(ctx context.Context, evt *events.Message) (*waE2E.Message, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil {
		return nil, fmt.Errorf("whatsapp client is not initialized")
	}
	return cli.DecryptSecretEncryptedMessage(ctx, evt)
}

func (c *Client) DeleteHistorySyncMedia(ctx context.Context, notif *waE2E.HistorySyncNotification) error {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil {
		return fmt.Errorf("whatsapp client is not initialized")
	}
	if notif == nil || notif.GetDirectPath() == "" {
		return nil
	}
	return cli.DeleteMedia(ctx, whatsmeow.MediaHistory, notif.GetDirectPath(), notif.GetFileEncSHA256(), notif.GetEncHandle())
}

func (c *Client) DecryptReaction(ctx context.Context, reaction *events.Message) (*waProto.ReactionMessage, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return nil, fmt.Errorf("not connected")
	}
	return cli.DecryptReaction(ctx, reaction)
}

func (c *Client) ParseWebMessage(chatJID types.JID, webMsg *waWeb.WebMessageInfo) (*events.Message, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil {
		return nil, fmt.Errorf("whatsapp client is not initialized")
	}
	return cli.ParseWebMessage(chatJID, webMsg)
}

func (c *Client) SetManualHistorySyncDownload(enabled bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != nil {
		c.client.ManualHistorySyncDownload = enabled
	}
}

func (c *Client) DownloadHistorySync(ctx context.Context, notif *waE2E.HistorySyncNotification) (*waHistorySync.HistorySync, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil {
		return nil, fmt.Errorf("whatsapp client is not initialized")
	}
	return cli.DownloadHistorySync(ctx, notif, true)
}

func (c *Client) RequestHistorySyncOnDemand(ctx context.Context, lastKnown types.MessageInfo, count int) (types.MessageID, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return "", fmt.Errorf("not connected")
	}
	if count <= 0 {
		count = 50
	}
	if lastKnown.Chat.IsEmpty() || strings.TrimSpace(string(lastKnown.ID)) == "" || lastKnown.Timestamp.IsZero() {
		return "", fmt.Errorf("invalid last known message info")
	}

	ownID := types.JID{}
	if cli.Store != nil && cli.Store.ID != nil {
		ownID = cli.Store.ID.ToNonAD()
	}
	if ownID.IsEmpty() {
		return "", fmt.Errorf("not authenticated; run `wacli auth`")
	}

	msg := cli.BuildHistorySyncRequest(&lastKnown, count)
	resp, err := cli.SendMessage(ctx, ownID, msg, whatsmeow.SendRequestExtra{Peer: true})
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

func (c *Client) RequestAppStateRecovery(ctx context.Context, name string) (types.MessageID, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return "", fmt.Errorf("not connected")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("app state collection name is required")
	}

	resp, err := cli.SendPeerMessage(ctx, whatsmeow.BuildAppStateRecoveryRequest(appstate.WAPatchName(name)))
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

func (c *Client) FetchAppState(ctx context.Context, name string, fullSync, onlyIfNotSynced bool) error {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return fmt.Errorf("not connected")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("app state collection name is required")
	}
	return cli.FetchAppState(ctx, appstate.WAPatchName(name), fullSync, onlyIfNotSynced)
}

// FetchAppStateEvents fetches one collection without globally dispatching the
// resulting events, so callers can persist that exact collection atomically
// with their own recovery marker protocol.
func (c *Client) FetchAppStateEvents(ctx context.Context, name string, fullSync, onlyIfNotSynced bool) ([]any, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return nil, fmt.Errorf("not connected")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("app state collection name is required")
	}
	if fullSync && !cli.EmitAppStateEventsOnFullSync {
		return nil, fmt.Errorf("full app state replay mutation emission is disabled")
	}
	return cli.DangerousInternals().FetchAppState(ctx, appstate.WAPatchName(name), fullSync, onlyIfNotSynced)
}

func (c *Client) GetGroupInfo(ctx context.Context, jid types.JID) (*types.GroupInfo, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return nil, fmt.Errorf("not connected")
	}
	return cli.GetGroupInfo(ctx, jid)
}

// SendChatPresence sends a typing or paused indicator to a chat.
func (c *Client) SendChatPresence(ctx context.Context, jid types.JID, state types.ChatPresence, media types.ChatPresenceMedia) error {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return fmt.Errorf("not connected")
	}
	return cli.SendChatPresence(ctx, jid, state, media)
}

func sendInitialAvailablePresence(ctx context.Context, cli *whatsmeow.Client) {
	// Whatsmeow recommends this once after connect so the server records the linked-device pushname.
	if cli == nil || cli.Store == nil || strings.TrimSpace(cli.Store.PushName) == "" {
		return
	}
	if err := cli.SendPresence(ctx, types.PresenceAvailable); err != nil {
		fmt.Fprintf(os.Stderr, "warn: failed to send initial available presence: %v\n", err)
	}
}

// SendPresence updates the authenticated account's global presence status.
func (c *Client) SendPresence(ctx context.Context, presence types.Presence) error {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return fmt.Errorf("not connected")
	}
	return cli.SendPresence(ctx, presence)
}

func (c *Client) Logout(ctx context.Context) error {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil {
		return fmt.Errorf("not initialized")
	}
	return cli.Logout(ctx)
}

// Reconnect loop helper.
func (c *Client) ReconnectWithBackoff(ctx context.Context, minDelay, maxDelay time.Duration, opts ConnectOptions) error {
	opts.AllowQR = false
	opts.DetachSocket = true
	delay := minDelay
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := c.Connect(ctx, opts); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		delay *= 2
		if delay > maxDelay {
			delay = maxDelay
		}
	}
}

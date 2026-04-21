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
	waProto "go.mau.fi/whatsmeow/binary/proto"
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

func (c *Client) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.client != nil && c.client.IsConnected()
}

type ConnectOptions struct {
	AllowQR  bool
	OnQRCode func(code string)
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
	if !authed && !opts.AllowQR {
		return fmt.Errorf("not authenticated; run `wacli auth`")
	}

	var qrChan <-chan whatsmeow.QRChannelItem
	if !authed {
		ch, _ := cli.GetQRChannel(ctx)
		qrChan = ch
	}

	if err := cli.ConnectContext(ctx); err != nil {
		return err
	}

	if authed {
		return nil
	}

	// Wait for QR flow to succeed or fail.
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case evt, ok := <-qrChan:
			if !ok {
				return fmt.Errorf("QR channel closed")
			}
			switch evt.Event {
			case "code":
				if opts.OnQRCode != nil {
					opts.OnQRCode(evt.Code)
				} else {
					qrterminal.GenerateHalfBlock(evt.Code, qrterminal.M, os.Stdout)
				}
			case "success":
				return nil
			case "timeout":
				return fmt.Errorf("QR code timed out")
			case "error":
				return fmt.Errorf("QR error")
			}
		}
	}
}

func (c *Client) AddEventHandler(handler func(interface{})) uint32 {
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

func (c *Client) SendText(ctx context.Context, to types.JID, text string) (types.MessageID, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return "", fmt.Errorf("not connected")
	}
	msg := &waProto.Message{Conversation: &text}
	resp, err := cli.SendMessage(ctx, to, msg)
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

func (c *Client) SendProtoMessage(ctx context.Context, to types.JID, msg *waProto.Message) (types.MessageID, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return "", fmt.Errorf("not connected")
	}
	resp, err := cli.SendMessage(ctx, to, msg)
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

func (c *Client) Upload(ctx context.Context, data []byte, mediaType whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return whatsmeow.UploadResponse{}, fmt.Errorf("not connected")
	}
	return cli.Upload(ctx, data, mediaType)
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

func (c *Client) GetContact(ctx context.Context, jid types.JID) (types.ContactInfo, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || cli.Store == nil || cli.Store.Contacts == nil {
		return types.ContactInfo{}, fmt.Errorf("contacts store not available")
	}
	return cli.Store.Contacts.GetContact(ctx, jid)
}

func (c *Client) GetAllContacts(ctx context.Context) (map[types.JID]types.ContactInfo, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || cli.Store == nil || cli.Store.Contacts == nil {
		return nil, fmt.Errorf("contacts store not available")
	}
	return cli.Store.Contacts.GetAllContacts(ctx)
}

func BestContactName(info types.ContactInfo) string {
	if !info.Found {
		return ""
	}
	if s := strings.TrimSpace(info.FullName); s != "" {
		return s
	}
	if s := strings.TrimSpace(info.FirstName); s != "" {
		return s
	}
	if s := strings.TrimSpace(info.BusinessName); s != "" {
		return s
	}
	if s := strings.TrimSpace(info.PushName); s != "" && s != "-" {
		return s
	}
	if s := strings.TrimSpace(info.RedactedPhone); s != "" {
		return s
	}
	return ""
}

func (c *Client) ResolveChatName(ctx context.Context, chat types.JID, pushName string) string {
	fallback := chat.String()

	if chat.Server == types.GroupServer || chat.IsBroadcastList() {
		info, err := c.GetGroupInfo(ctx, chat)
		if err == nil && info != nil {
			if name := strings.TrimSpace(info.GroupName.Name); name != "" {
				return name
			}
		}
	} else {
		info, err := c.GetContact(ctx, chat.ToNonAD())
		if err == nil {
			if name := BestContactName(info); name != "" {
				return name
			}
		}
	}

	if name := strings.TrimSpace(pushName); name != "" && name != "-" {
		return name
	}
	return fallback
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
func (c *Client) ReconnectWithBackoff(ctx context.Context, minDelay, maxDelay time.Duration) error {
	delay := minDelay
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := c.Connect(ctx, ConnectOptions{AllowQR: false}); err == nil {
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

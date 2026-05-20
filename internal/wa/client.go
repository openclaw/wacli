package wa

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mdp/qrterminal/v3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	waBinary "go.mau.fi/whatsmeow/binary"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
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
	AllowQR         bool
	OnQRCode        func(code string)
	PairPhoneNumber string
	OnPairCode      func(code string)
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

	if err := cli.ConnectContext(ctx); err != nil {
		return err
	}

	if authed {
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
	case evt.Event == whatsmeow.QRChannelEventError:
		if evt.Error != nil {
			return fmt.Errorf("QR pairing failed: %w", evt.Error)
		}
		return fmt.Errorf("QR pairing failed")
	default:
		return nil
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
	return c.SendProtoMessageWithExtra(ctx, to, msg, "")
}

func (c *Client) SendProtoMessageWithExtra(ctx context.Context, to types.JID, msg *waProto.Message, mediaHandle string) (types.MessageID, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return "", fmt.Errorf("not connected")
	}
	if mediaHandle == "" {
		resp, err := cli.SendMessage(ctx, to, msg)
		if err != nil {
			return "", err
		}
		return resp.ID, nil
	}
	resp, err := cli.SendMessage(ctx, to, msg, whatsmeow.SendRequestExtra{MediaHandle: mediaHandle})
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

// SendPoll builds a PollCreationMessage and sends it. selectable is the
// maximum number of options a voter may pick (1 = single-select). The poll
// can optionally be wrapped in an EphemeralMessage for disappearing chats.
func (c *Client) SendPoll(ctx context.Context, to types.JID, name string, options []string, selectable int, ephemeral bool) (types.MessageID, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return "", fmt.Errorf("not connected")
	}
	var groupInfo *types.GroupInfo
	if to.Server == types.GroupServer {
		groupInfo, _ = cli.GetGroupInfo(ctx, to)
	}
	msg := buildPollCreationMessage(name, options, selectable, isCommunityAnnouncementGroup(groupInfo))
	if ephemeral {
		msg = wrapEphemeralPollMessage(msg)
	}
	resp, err := cli.SendMessage(ctx, to, msg)
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

func buildPollCreationMessage(name string, optionNames []string, selectableOptionCount int, toAnnouncementGroup bool) *waE2E.Message {
	msgSecret := make([]byte, 32)
	_, _ = rand.Read(msgSecret)
	if selectableOptionCount < 0 || selectableOptionCount > len(optionNames) {
		selectableOptionCount = 0
	}
	options := make([]*waE2E.PollCreationMessage_Option, len(optionNames))
	for i, option := range optionNames {
		options[i] = &waE2E.PollCreationMessage_Option{OptionName: proto.String(option)}
	}
	creation := &waE2E.PollCreationMessage{
		Name:                   proto.String(name),
		Options:                options,
		SelectableOptionsCount: proto.Uint32(uint32(selectableOptionCount)),
	}
	msg := &waE2E.Message{
		MessageContextInfo: &waE2E.MessageContextInfo{
			MessageSecret: msgSecret,
		},
	}
	switch {
	case toAnnouncementGroup:
		msg.PollCreationMessageV2 = creation
	case selectableOptionCount == 1:
		msg.PollCreationMessageV3 = creation
	default:
		msg.PollCreationMessage = creation
	}
	return msg
}

func isCommunityAnnouncementGroup(info *types.GroupInfo) bool {
	return info != nil && info.IsAnnounce && info.IsParent
}

func pickOutboundPollCreation(msg *waE2E.Message) *waE2E.PollCreationMessage {
	if msg == nil {
		return nil
	}
	if msg.GetPollCreationMessage() != nil {
		return msg.GetPollCreationMessage()
	}
	if msg.GetPollCreationMessageV2() != nil {
		return msg.GetPollCreationMessageV2()
	}
	return msg.GetPollCreationMessageV3()
}

func wrapEphemeralPollMessage(msg *waE2E.Message) *waE2E.Message {
	if msg == nil {
		return nil
	}
	return &waE2E.Message{
		EphemeralMessage:   &waE2E.FutureProofMessage{Message: msg},
		MessageContextInfo: msg.MessageContextInfo,
	}
}

// SendPollVote builds and sends a poll vote for the poll identified by
// pollInfo (Chat, Sender, ID of the original PollCreationMessage). The
// option names must match exactly the strings used in the poll.
//
// On migrated DM accounts, whatsmeow's SendMessage auto-rewrites the
// destination from a phone-number JID to the corresponding LID. Pre-translate
// DMs so the PollCreationMessageKey embedded by BuildPollVote matches the
// chat/sender on the wire.
func (c *Client) SendPollVote(ctx context.Context, pollInfo *types.MessageInfo, options []string) (types.MessageID, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return "", fmt.Errorf("not connected")
	}
	if pollInfo == nil {
		return "", fmt.Errorf("poll info is required")
	}

	info := *pollInfo
	info = rewritePollVoteInfoForLID(ctx, cli, info, c.resolvePNToLIDLocked)

	msg, err := cli.BuildPollVote(ctx, &info, options)
	if err != nil {
		return "", fmt.Errorf("build poll vote: %w", err)
	}
	resp, err := cli.SendMessage(ctx, info.Chat, msg)
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

type pollVoteLIDResolver func(context.Context, *whatsmeow.Client, types.JID) types.JID

func rewritePollVoteInfoForLID(ctx context.Context, cli *whatsmeow.Client, info types.MessageInfo, resolve pollVoteLIDResolver) types.MessageInfo {
	if cli == nil || cli.Store == nil || cli.Store.LIDMigrationTimestamp <= 0 || resolve == nil {
		return info
	}
	switch info.Chat.Server {
	case types.DefaultUserServer:
		info.Chat = resolve(ctx, cli, info.Chat)
		if info.Sender.Server == types.DefaultUserServer {
			info.Sender = resolve(ctx, cli, info.Sender)
		}
	case types.HiddenUserServer:
		if info.Sender.Server == types.DefaultUserServer {
			info.Sender = resolve(ctx, cli, info.Sender)
		}
	}
	return info
}

// resolvePNToLIDLocked translates a phone-number JID to its LID counterpart
// using the active session store; falls back to the input JID if no mapping
// exists. Caller already holds (or doesn't need) c.mu.
func (c *Client) resolvePNToLIDLocked(ctx context.Context, cli *whatsmeow.Client, jid types.JID) types.JID {
	if cli == nil || cli.Store == nil {
		return jid
	}
	pn := jid.ToNonAD()
	if ownPN := cli.Store.GetJID().ToNonAD(); pn == ownPN {
		if ownLID := cli.Store.GetLID().ToNonAD(); !ownLID.IsEmpty() {
			return ownLID
		}
	}
	if cli.Store.LIDs == nil {
		return jid
	}
	lid, err := cli.Store.LIDs.GetLIDForPN(ctx, pn)
	if err == nil && !lid.IsEmpty() {
		return lid
	}
	info, err := cli.GetUserInfo(ctx, []types.JID{pn})
	if err == nil {
		if resolved := info[pn].LID.ToNonAD(); !resolved.IsEmpty() {
			return resolved
		}
	}
	return jid
}

// DecryptPollVote decrypts an incoming PollUpdateMessage event and returns
// the SHA-256 hashes of the selected options. The caller is responsible for
// matching those hashes back to option names.
func (c *Client) DecryptPollVote(ctx context.Context, evt *events.Message) (*waE2E.PollVoteMessage, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil {
		return nil, fmt.Errorf("whatsapp client is not initialized")
	}
	return cli.DecryptPollVote(ctx, evt)
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

func (c *Client) SendReaction(ctx context.Context, chat, sender types.JID, targetID types.MessageID, reaction string) (types.MessageID, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return "", fmt.Errorf("not connected")
	}
	resp, err := cli.SendMessage(ctx, chat, cli.BuildReaction(chat, sender, targetID, reaction))
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

func (c *Client) RevokeMessage(ctx context.Context, chat types.JID, targetID types.MessageID) (types.MessageID, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return "", fmt.Errorf("not connected")
	}
	resp, err := cli.SendMessage(ctx, chat, cli.BuildRevoke(chat, types.EmptyJID, targetID))
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

func (c *Client) DeleteMessageForMe(ctx context.Context, info types.MessageInfo, deleteMedia bool) error {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return fmt.Errorf("not connected")
	}
	if info.Chat.IsEmpty() || strings.TrimSpace(string(info.ID)) == "" {
		return fmt.Errorf("message chat and ID are required")
	}
	return cli.SendAppState(ctx, buildDeleteForMePatch(info, deleteMedia))
}

func buildDeleteForMePatch(info types.MessageInfo, deleteMedia bool) appstate.PatchInfo {
	fromMe := "0"
	if info.IsFromMe {
		fromMe = "1"
	}
	sender := "0"
	if !info.IsFromMe && !info.Sender.IsEmpty() && info.Chat.User != info.Sender.User {
		sender = info.Sender.String()
	}
	return appstate.PatchInfo{
		Type: appstate.WAPatchRegularHigh,
		Mutations: []appstate.MutationInfo{{
			Index:   []string{appstate.IndexDeleteMessageForMe, info.Chat.String(), string(info.ID), fromMe, sender},
			Version: 2,
			Value: &waSyncAction.SyncActionValue{
				DeleteMessageForMeAction: &waSyncAction.DeleteMessageForMeAction{
					DeleteMedia:      proto.Bool(deleteMedia),
					MessageTimestamp: proto.Int64(info.Timestamp.UnixMilli()),
				},
			},
		}},
	}
}

func (c *Client) EditMessage(ctx context.Context, chat types.JID, targetID types.MessageID, text string) (types.MessageID, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return "", fmt.Errorf("not connected")
	}
	msg := &waE2E.Message{Conversation: proto.String(text)}
	resp, err := cli.SendMessage(ctx, chat, cli.BuildEdit(chat, targetID, msg))
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

func (c *Client) GetUserInfo(ctx context.Context, jids []types.JID) (map[types.JID]types.UserInfo, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return nil, fmt.Errorf("not connected")
	}
	return cli.GetUserInfo(ctx, jids)
}

func (c *Client) IsOnWhatsApp(ctx context.Context, phones []string) ([]types.IsOnWhatsAppResponse, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return nil, fmt.Errorf("not connected")
	}
	return cli.IsOnWhatsApp(ctx, phones)
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

func (c *Client) ResolveLIDToPN(ctx context.Context, jid types.JID) types.JID {
	if jid.Server != types.HiddenUserServer {
		return jid
	}
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || cli.Store == nil || cli.Store.LIDs == nil {
		return jid
	}
	pn, err := cli.Store.LIDs.GetPNForLID(ctx, jid.ToNonAD())
	if err != nil || pn.IsEmpty() {
		return jid
	}
	return pn
}

func (c *Client) ResolvePNToLID(ctx context.Context, jid types.JID) types.JID {
	if jid.Server != types.DefaultUserServer {
		return jid
	}
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	return c.resolvePNToLIDLocked(ctx, cli, jid)
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

	if chat.Server == types.NewsletterServer {
		meta, err := c.GetNewsletterInfo(ctx, chat)
		if err == nil && meta != nil {
			if name := NewsletterName(meta); name != "" {
				return name
			}
		}
	} else if chat.Server == types.GroupServer || chat.IsBroadcastList() {
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

func sendInitialAvailablePresence(ctx context.Context, cli *whatsmeow.Client) {
	// Whatsmeow recommends this once after connect so the server records the linked-device pushname.
	if cli == nil || cli.Store == nil || strings.TrimSpace(cli.Store.PushName) == "" {
		return
	}
	if err := cli.SendPresence(ctx, types.PresenceAvailable); err != nil {
		fmt.Fprintf(os.Stderr, "warn: failed to send initial available presence: %v\n", err)
	}
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

// SetProfilePicture sets the profile picture of the authenticated account.
// avatar must be JPEG bytes; pass nil to remove the picture.
// Returns the new picture ID assigned by WhatsApp.
//
// Uses DangerousInternals.SendIQ to send the w:profile:picture IQ stanza
// without a "target" attribute, which is the correct format for updating
// your own profile picture (as opposed to SetGroupPhoto which always sets target).
func (c *Client) SetProfilePicture(ctx context.Context, avatar []byte) (string, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return "", fmt.Errorf("not connected")
	}

	var content interface{}
	if avatar != nil {
		content = []waBinary.Node{{
			Tag:     "picture",
			Attrs:   waBinary.Attrs{"type": "image"},
			Content: avatar,
		}}
	}

	resp, err := cli.DangerousInternals().SendIQ(ctx, whatsmeow.DangerousInfoQuery{
		Namespace: "w:profile:picture",
		Type:      "set",
		To:        types.ServerJID,
		Content:   content,
	})
	if err != nil {
		return "", err
	}
	if avatar == nil {
		return "remove", nil
	}
	pictureID, ok := resp.GetChildByTag("picture").Attrs["id"].(string)
	if !ok {
		return "", fmt.Errorf("no picture ID in response")
	}
	return pictureID, nil
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

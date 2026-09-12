package wa

import (
	"context"
	"crypto/rand"
	"fmt"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

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
	info = rewritePollVoteInfoForLID(ctx, cli, info, resolvePNToLID)

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

package wa

import (
	"context"
	"fmt"
	"strings"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

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

package main

import (
	"context"
	"time"

	"github.com/steipete/wacli/internal/app"
	"github.com/steipete/wacli/internal/store"
	"go.mau.fi/whatsmeow/types"
)

func sendReact(ctx context.Context, a interface {
	WA() app.WAClient
	DB() *store.DB
}, chatJID types.JID, messageID, emoji string) (string, error) {
	resp, err := a.WA().SendReaction(ctx, chatJID, messageID, emoji)
	if err != nil {
		return "", err
	}

	now := time.Now().UTC()
	chatName := a.WA().ResolveChatName(ctx, chatJID, "")
	kind := chatKindFromJID(chatJID)
	_ = a.DB().UpsertChat(chatJID.String(), kind, chatName, now)

	text := emoji
	if text == "" {
		text = "[reaction removed]"
	}
	_ = a.DB().UpsertMessage(store.UpsertMessageParams{
		ChatJID:    chatJID.String(),
		ChatName:   chatName,
		MsgID:      string(resp.ID),
		SenderJID:  "",
		SenderName: "me",
		Timestamp:  now,
		FromMe:     true,
		Text:       text,
	})

	return string(resp.ID), nil
}

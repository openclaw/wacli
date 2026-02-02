package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steipete/wacli/internal/app"
	"github.com/steipete/wacli/internal/store"
	"github.com/steipete/wacli/internal/wa"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

func sendSticker(ctx context.Context, a interface {
	WA() app.WAClient
	DB() *store.DB
}, to types.JID, filePath string) (string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}

	// Validate file extension - stickers must be WebP
	ext := strings.ToLower(filepath.Ext(filePath))
	if ext != ".webp" {
		return "", fmt.Errorf("stickers must be WebP files (got %s)", ext)
	}

	uploadType, _ := wa.MediaTypeFromString("sticker")
	up, err := a.WA().Upload(ctx, data, uploadType)
	if err != nil {
		return "", err
	}

	msg := &waProto.Message{
		StickerMessage: &waProto.StickerMessage{
			URL:           proto.String(up.URL),
			DirectPath:    proto.String(up.DirectPath),
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    proto.Uint64(up.FileLength),
			Mimetype:      proto.String("image/webp"),
		},
	}

	id, err := a.WA().SendProtoMessage(ctx, to, msg)
	if err != nil {
		return "", err
	}

	now := time.Now().UTC()
	chatName := a.WA().ResolveChatName(ctx, to, "")
	kind := chatKindFromJID(to)
	_ = a.DB().UpsertChat(to.String(), kind, chatName, now)
	_ = a.DB().UpsertMessage(store.UpsertMessageParams{
		ChatJID:       to.String(),
		ChatName:      chatName,
		MsgID:         id,
		SenderJID:     "",
		SenderName:    "me",
		Timestamp:     now,
		FromMe:        true,
		Text:          "",
		MediaType:     "sticker",
		MimeType:      "image/webp",
		DirectPath:    up.DirectPath,
		MediaKey:      up.MediaKey,
		FileSHA256:    up.FileSHA256,
		FileEncSHA256: up.FileEncSHA256,
		FileLength:    up.FileLength,
	})

	return id, nil
}

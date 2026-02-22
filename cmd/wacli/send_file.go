package main

import (
	"context"
	"mime"
	"net/http"
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

func sendFile(ctx context.Context, a interface {
	WA() app.WAClient
	DB() *store.DB
}, to types.JID, filePath, filename, caption, mimeOverride string) (string, map[string]string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", nil, err
	}

	name := strings.TrimSpace(filename)
	if name == "" {
		name = filepath.Base(filePath)
	}
	mimeType := strings.TrimSpace(mimeOverride)
	if mimeType == "" {
		// Use filePath for MIME detection, not the display name override
		mimeType = mime.TypeByExtension(strings.ToLower(filepath.Ext(filePath)))
	}
	if mimeType == "" {
		sniff := data
		if len(sniff) > 512 {
			sniff = sniff[:512]
		}
		mimeType = http.DetectContentType(sniff)
	}

	mediaType := "document"
	uploadType, _ := wa.MediaTypeFromString("document")
	switch {
	case strings.HasPrefix(mimeType, "image/"):
		mediaType = "image"
		uploadType, _ = wa.MediaTypeFromString("image")
	case strings.HasPrefix(mimeType, "video/"):
		mediaType = "video"
		uploadType, _ = wa.MediaTypeFromString("video")
	case strings.HasPrefix(mimeType, "audio/"):
		mediaType = "audio"
		uploadType, _ = wa.MediaTypeFromString("audio")
	}

	var up struct {
		URL           string
		DirectPath    string
		MediaKey      []byte
		FileEncSHA256 []byte
		FileSHA256    []byte
		FileLength    uint64
		Handle        string
	}
	isNewsletter := to.Server == types.NewsletterServer
	if isNewsletter {
		resp, err := a.WA().UploadNewsletter(ctx, data, uploadType)
		if err != nil {
			return "", nil, err
		}
		up.URL = resp.URL
		up.DirectPath = resp.DirectPath
		up.FileSHA256 = resp.FileSHA256
		up.FileLength = resp.FileLength
		up.Handle = resp.Handle
	} else {
		resp, err := a.WA().Upload(ctx, data, uploadType)
		if err != nil {
			return "", nil, err
		}
		up.URL = resp.URL
		up.DirectPath = resp.DirectPath
		up.MediaKey = resp.MediaKey
		up.FileEncSHA256 = resp.FileEncSHA256
		up.FileSHA256 = resp.FileSHA256
		up.FileLength = resp.FileLength
	}

	now := time.Now().UTC()
	msg := &waProto.Message{}

	if isNewsletter {
		switch mediaType {
		case "image":
			msg.ImageMessage = &waProto.ImageMessage{
				URL:        proto.String(up.URL),
				DirectPath: proto.String(up.DirectPath),
				FileSHA256: up.FileSHA256,
				FileLength: proto.Uint64(up.FileLength),
				Mimetype:   proto.String(mimeType),
				Caption:    proto.String(caption),
			}
		case "video":
			msg.VideoMessage = &waProto.VideoMessage{
				URL:        proto.String(up.URL),
				DirectPath: proto.String(up.DirectPath),
				FileSHA256: up.FileSHA256,
				FileLength: proto.Uint64(up.FileLength),
				Mimetype:   proto.String(mimeType),
				Caption:    proto.String(caption),
			}
		case "audio":
			msg.AudioMessage = &waProto.AudioMessage{
				URL:        proto.String(up.URL),
				DirectPath: proto.String(up.DirectPath),
				FileSHA256: up.FileSHA256,
				FileLength: proto.Uint64(up.FileLength),
				Mimetype:   proto.String(mimeType),
				PTT:        proto.Bool(false),
			}
		default:
			msg.DocumentMessage = &waProto.DocumentMessage{
				URL:        proto.String(up.URL),
				DirectPath: proto.String(up.DirectPath),
				FileSHA256: up.FileSHA256,
				FileLength: proto.Uint64(up.FileLength),
				Mimetype:   proto.String(mimeType),
				FileName:   proto.String(name),
				Caption:    proto.String(caption),
				Title:      proto.String(name),
			}
		}
		id, err := a.WA().SendProtoMessageWithExtra(ctx, to, msg, up.Handle)
		if err != nil {
			return "", nil, err
		}
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
			Text:          caption,
			MediaType:     mediaType,
			MediaCaption:  caption,
			Filename:      name,
			MimeType:      mimeType,
			DirectPath:    up.DirectPath,
			MediaKey:      up.MediaKey,
			FileSHA256:    up.FileSHA256,
			FileEncSHA256: up.FileEncSHA256,
			FileLength:    up.FileLength,
		})
		return id, map[string]string{
			"name":      name,
			"mime_type": mimeType,
			"media":     mediaType,
		}, nil
	}

	switch mediaType {
	case "image":
		msg.ImageMessage = &waProto.ImageMessage{
			URL:           proto.String(up.URL),
			DirectPath:    proto.String(up.DirectPath),
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    proto.Uint64(up.FileLength),
			Mimetype:      proto.String(mimeType),
			Caption:       proto.String(caption),
		}
	case "video":
		msg.VideoMessage = &waProto.VideoMessage{
			URL:           proto.String(up.URL),
			DirectPath:    proto.String(up.DirectPath),
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    proto.Uint64(up.FileLength),
			Mimetype:      proto.String(mimeType),
			Caption:       proto.String(caption),
		}
	case "audio":
		msg.AudioMessage = &waProto.AudioMessage{
			URL:           proto.String(up.URL),
			DirectPath:    proto.String(up.DirectPath),
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    proto.Uint64(up.FileLength),
			Mimetype:      proto.String(mimeType),
			PTT:           proto.Bool(false),
		}
	default:
		msg.DocumentMessage = &waProto.DocumentMessage{
			URL:           proto.String(up.URL),
			DirectPath:    proto.String(up.DirectPath),
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    proto.Uint64(up.FileLength),
			Mimetype:      proto.String(mimeType),
			FileName:      proto.String(name),
			Caption:       proto.String(caption),
			Title:         proto.String(name),
		}
	}

	id, err := a.WA().SendProtoMessage(ctx, to, msg)
	if err != nil {
		return "", nil, err
	}

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
		Text:          caption,
		MediaType:     mediaType,
		MediaCaption:  caption,
		Filename:      name,
		MimeType:      mimeType,
		DirectPath:    up.DirectPath,
		MediaKey:      up.MediaKey,
		FileSHA256:    up.FileSHA256,
		FileEncSHA256: up.FileEncSHA256,
		FileLength:    up.FileLength,
	})

	return id, map[string]string{
		"name":      name,
		"mime_type": mimeType,
		"media":     mediaType,
	}, nil
}

func chatKindFromJID(j types.JID) string {
	if j.Server == types.NewsletterServer {
		return "newsletter"
	}
	if j.Server == types.GroupServer {
		return "group"
	}
	if j.IsBroadcastList() {
		return "broadcast"
	}
	if j.Server == types.DefaultUserServer {
		return "dm"
	}
	return "unknown"
}

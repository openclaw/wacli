package app

import (
	"context"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steipete/wacli/internal/store"
	"github.com/steipete/wacli/internal/wa"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// SendTextParams contains parameters for sending a text message.
type SendTextParams struct {
	To      string
	Message string
}

// SendFileParams contains parameters for sending a file.
type SendFileParams struct {
	To       string
	FilePath string
	Filename string
	Caption  string
	MIMEType string
}

// SendFileMetadata describes the sent file.
type SendFileMetadata struct {
	Name     string
	MIMEType string
	Media    string
}

// SendResult is the result of a successful send.
type SendResult struct {
	To   string
	ID   string
	File *SendFileMetadata
}

// SendText sends a text message to the given recipient.
func (a *App) SendText(ctx context.Context, p SendTextParams) (SendResult, error) {
	if err := a.ensureReadyToSend(ctx); err != nil {
		return SendResult{}, err
	}

	toJID, err := wa.ParseUserOrJID(p.To)
	if err != nil {
		return SendResult{}, err
	}

	msgID, err := a.wa.SendText(ctx, toJID, p.Message)
	if err != nil {
		return SendResult{}, err
	}

	now := time.Now().UTC()
	chatName := a.wa.ResolveChatName(ctx, toJID, "")
	a.persistSentMessage(toJID, chatName, now, store.UpsertMessageParams{
		ChatJID:    toJID.String(),
		ChatName:   chatName,
		MsgID:      string(msgID),
		SenderJID:  "",
		SenderName: "me",
		Timestamp:  now,
		FromMe:     true,
		Text:       p.Message,
	})

	return SendResult{
		To: toJID.String(),
		ID: string(msgID),
	}, nil
}

// SendFile sends a file to the given recipient.
func (a *App) SendFile(ctx context.Context, p SendFileParams) (SendResult, error) {
	if err := a.ensureReadyToSend(ctx); err != nil {
		return SendResult{}, err
	}

	toJID, err := wa.ParseUserOrJID(p.To)
	if err != nil {
		return SendResult{}, err
	}

	data, err := os.ReadFile(p.FilePath)
	if err != nil {
		return SendResult{}, err
	}

	resolvedName, mimeType, mediaType, uploadType := detectOutboundFile(p.FilePath, p.Filename, p.MIMEType, data)

	up, err := a.wa.Upload(ctx, data, uploadType)
	if err != nil {
		return SendResult{}, err
	}

	msg := buildOutboundMediaMessage(mediaType, resolvedName, mimeType, p.Caption, up)

	msgID, err := a.wa.SendProtoMessage(ctx, toJID, msg)
	if err != nil {
		return SendResult{}, err
	}

	now := time.Now().UTC()
	chatName := a.wa.ResolveChatName(ctx, toJID, "")
	a.persistSentMessage(toJID, chatName, now, store.UpsertMessageParams{
		ChatJID:       toJID.String(),
		ChatName:      chatName,
		MsgID:         string(msgID),
		SenderJID:     "",
		SenderName:    "me",
		Timestamp:     now,
		FromMe:        true,
		Text:          p.Caption,
		MediaType:     mediaType,
		MediaCaption:  p.Caption,
		Filename:      resolvedName,
		MimeType:      mimeType,
		DirectPath:    up.DirectPath,
		MediaKey:      up.MediaKey,
		FileSHA256:    up.FileSHA256,
		FileEncSHA256: up.FileEncSHA256,
		FileLength:    up.FileLength,
	})

	return SendResult{
		To: toJID.String(),
		ID: string(msgID),
		File: &SendFileMetadata{
			Name:     resolvedName,
			MIMEType: mimeType,
			Media:    mediaType,
		},
	}, nil
}

func (a *App) ensureReadyToSend(ctx context.Context) error {
	if err := a.EnsureAuthed(); err != nil {
		return err
	}
	return a.Connect(ctx, false, nil)
}

func (a *App) persistSentMessage(to types.JID, chatName string, now time.Time, params store.UpsertMessageParams) {
	kind := chatKind(to)
	_ = a.db.UpsertChat(to.String(), kind, chatName, now)
	_ = a.db.UpsertMessage(params)
}

func detectOutboundFile(filePath, filename, mimeOverride string, data []byte) (resolvedName, mimeType, mediaType string, uploadType whatsmeow.MediaType) {
	resolvedName = strings.TrimSpace(filename)
	if resolvedName == "" {
		resolvedName = filepath.Base(filePath)
	}

	mimeType = strings.TrimSpace(mimeOverride)
	if mimeType == "" {
		mimeType = mime.TypeByExtension(strings.ToLower(filepath.Ext(filePath)))
	}
	if mimeType == "" {
		sniff := data
		if len(sniff) > 512 {
			sniff = sniff[:512]
		}
		mimeType = http.DetectContentType(sniff)
	}

	mediaType = "document"
	uploadType, _ = wa.MediaTypeFromString("document")
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

	return
}

func buildOutboundMediaMessage(mediaType, resolvedName, mimeType, caption string, up whatsmeow.UploadResponse) *waProto.Message {
	msg := &waProto.Message{}

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
			FileName:      proto.String(resolvedName),
			Caption:       proto.String(caption),
			Title:         proto.String(resolvedName),
		}
	}

	return msg
}
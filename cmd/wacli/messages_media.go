package main

import (
	"fmt"
	"strings"

	"github.com/openclaw/wacli/internal/store"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"google.golang.org/protobuf/proto"
)

func buildStoredMediaMessage(mediaType, caption string, mediaInfo store.MediaDownloadInfo, ctx *waProto.ContextInfo) (*waProto.Message, error) {
	switch mediaType {
	case "image":
		return &waProto.Message{ImageMessage: &waProto.ImageMessage{
			DirectPath:    proto.String(mediaInfo.DirectPath),
			MediaKey:      mediaInfo.MediaKey,
			FileSHA256:    mediaInfo.FileSHA256,
			FileEncSHA256: mediaInfo.FileEncSHA256,
			FileLength:    proto.Uint64(mediaInfo.FileLength),
			Mimetype:      proto.String(mediaInfo.MimeType),
			Caption:       proto.String(caption),
			ContextInfo:   ctx,
		}}, nil
	case "video", "gif":
		return &waProto.Message{VideoMessage: &waProto.VideoMessage{
			DirectPath:    proto.String(mediaInfo.DirectPath),
			MediaKey:      mediaInfo.MediaKey,
			FileSHA256:    mediaInfo.FileSHA256,
			FileEncSHA256: mediaInfo.FileEncSHA256,
			FileLength:    proto.Uint64(mediaInfo.FileLength),
			Mimetype:      proto.String(mediaInfo.MimeType),
			Caption:       proto.String(caption),
			GifPlayback:   proto.Bool(mediaType == "gif"),
			ContextInfo:   ctx,
		}}, nil
	case "audio":
		return &waProto.Message{AudioMessage: &waProto.AudioMessage{
			DirectPath:    proto.String(mediaInfo.DirectPath),
			MediaKey:      mediaInfo.MediaKey,
			FileSHA256:    mediaInfo.FileSHA256,
			FileEncSHA256: mediaInfo.FileEncSHA256,
			FileLength:    proto.Uint64(mediaInfo.FileLength),
			Mimetype:      proto.String(mediaInfo.MimeType),
			ContextInfo:   ctx,
		}}, nil
	case "document":
		name := strings.TrimSpace(mediaInfo.Filename)
		if name == "" {
			name = "document"
		}
		return &waProto.Message{DocumentMessage: &waProto.DocumentMessage{
			DirectPath:    proto.String(mediaInfo.DirectPath),
			MediaKey:      mediaInfo.MediaKey,
			FileSHA256:    mediaInfo.FileSHA256,
			FileEncSHA256: mediaInfo.FileEncSHA256,
			FileLength:    proto.Uint64(mediaInfo.FileLength),
			Mimetype:      proto.String(mediaInfo.MimeType),
			FileName:      proto.String(name),
			Title:         proto.String(name),
			Caption:       proto.String(caption),
			ContextInfo:   ctx,
		}}, nil
	case "sticker":
		return &waProto.Message{StickerMessage: &waProto.StickerMessage{
			DirectPath:    proto.String(mediaInfo.DirectPath),
			MediaKey:      mediaInfo.MediaKey,
			FileSHA256:    mediaInfo.FileSHA256,
			FileEncSHA256: mediaInfo.FileEncSHA256,
			FileLength:    proto.Uint64(mediaInfo.FileLength),
			Mimetype:      proto.String(mediaInfo.MimeType),
			ContextInfo:   ctx,
		}}, nil
	default:
		return nil, fmt.Errorf("unsupported stored media type %q", mediaType)
	}
}

func isStoredMediaType(mediaType string) bool {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "image", "video", "gif", "audio", "document", "sticker":
		return true
	default:
		return false
	}
}

func validateStoredMediaInfo(info store.MediaDownloadInfo) error {
	if strings.TrimSpace(info.DirectPath) == "" || len(info.MediaKey) == 0 || len(info.FileSHA256) == 0 || len(info.FileEncSHA256) == 0 || info.FileLength == 0 {
		return fmt.Errorf("message has incomplete media metadata (run `wacli sync` first)")
	}
	if strings.TrimSpace(info.MimeType) == "" {
		return fmt.Errorf("message has incomplete media MIME metadata")
	}
	return nil
}

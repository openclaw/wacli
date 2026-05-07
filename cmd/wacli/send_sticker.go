package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"path/filepath"
	"time"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

const sendStickerMIME = "image/webp"

const (
	stickerDimension       = 512
	maxStaticStickerBytes  = 100 * 1024
	maxAnimatedStickerByte = 500 * 1024
)

type sendStickerOptions struct {
	replyTo       string
	replyToSender string
}

type webPStickerMetadata struct {
	width    uint32
	height   uint32
	animated bool
}

func sendSticker(ctx context.Context, a interface {
	WA() app.WAClient
	DB() *store.DB
}, to types.JID, filePath string, opts sendStickerOptions) (string, map[string]string, error) {
	data, err := readSendFileData(filePath)
	if err != nil {
		return "", nil, err
	}
	meta, err := validateWebPSticker(data)
	if err != nil {
		return "", nil, err
	}

	uploadType, err := wa.MediaTypeFromString("sticker")
	if err != nil {
		return "", nil, err
	}
	up, err := a.WA().Upload(ctx, data, uploadType)
	if err != nil {
		return "", nil, err
	}

	replyContext, err := buildReplyContextInfo(a.DB(), to, opts.replyTo, opts.replyToSender)
	if err != nil {
		return "", nil, err
	}
	msg := newStickerMessage(up, replyContext, meta)

	id, err := a.WA().SendProtoMessage(ctx, to, msg)
	if err != nil {
		return "", nil, err
	}

	now := time.Now().UTC()
	name := filepath.Base(filePath)
	chatName := a.WA().ResolveChatName(ctx, to, "")
	_ = a.DB().UpsertChat(to.String(), chatKindFromJID(to), chatName, now)
	_ = a.DB().UpsertMessage(store.UpsertMessageParams{
		ChatJID:       to.String(),
		ChatName:      chatName,
		MsgID:         id,
		SenderJID:     "",
		SenderName:    "me",
		Timestamp:     now,
		FromMe:        true,
		MediaType:     "sticker",
		Filename:      name,
		MimeType:      sendStickerMIME,
		DirectPath:    up.DirectPath,
		MediaKey:      up.MediaKey,
		FileSHA256:    up.FileSHA256,
		FileEncSHA256: up.FileEncSHA256,
		FileLength:    up.FileLength,
	})

	return id, map[string]string{
		"name":      name,
		"mime_type": sendStickerMIME,
		"media":     "sticker",
	}, nil
}

func newStickerMessage(up whatsmeow.UploadResponse, info *waProto.ContextInfo, meta webPStickerMetadata) *waProto.Message {
	return &waProto.Message{
		StickerMessage: &waProto.StickerMessage{
			URL:           proto.String(up.URL),
			DirectPath:    proto.String(up.DirectPath),
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    proto.Uint64(up.FileLength),
			Mimetype:      proto.String(sendStickerMIME),
			Height:        proto.Uint32(meta.height),
			Width:         proto.Uint32(meta.width),
			IsAnimated:    proto.Bool(meta.animated),
			ContextInfo:   info,
		},
	}
}

func isWebPStickerData(data []byte) bool {
	_, err := parseWebPStickerMetadata(data)
	return err == nil
}

func validateWebPSticker(data []byte) (webPStickerMetadata, error) {
	meta, err := parseWebPStickerMetadata(data)
	if err != nil {
		return webPStickerMetadata{}, fmt.Errorf("stickers must be valid WebP files")
	}
	if meta.width != stickerDimension || meta.height != stickerDimension {
		return webPStickerMetadata{}, fmt.Errorf("stickers must be %dx%d WebP files (got %dx%d)", stickerDimension, stickerDimension, meta.width, meta.height)
	}
	maxBytes := maxStaticStickerBytes
	kind := "static"
	if meta.animated {
		maxBytes = maxAnimatedStickerByte
		kind = "animated"
	}
	if len(data) > maxBytes {
		return webPStickerMetadata{}, fmt.Errorf("%s stickers must be at most %d KiB (got %d KiB)", kind, maxBytes/1024, (len(data)+1023)/1024)
	}
	return meta, nil
}

func parseWebPStickerMetadata(data []byte) (webPStickerMetadata, error) {
	if len(data) < 12 || !bytes.Equal(data[0:4], []byte("RIFF")) || !bytes.Equal(data[8:12], []byte("WEBP")) {
		return webPStickerMetadata{}, fmt.Errorf("missing WebP header")
	}
	for off := 12; off+8 <= len(data); {
		chunkType := string(data[off : off+4])
		chunkSize := int(binary.LittleEndian.Uint32(data[off+4 : off+8]))
		chunkStart := off + 8
		chunkEnd := chunkStart + chunkSize
		if chunkSize < 0 || chunkEnd > len(data) {
			return webPStickerMetadata{}, fmt.Errorf("invalid WebP chunk size")
		}
		chunk := data[chunkStart:chunkEnd]
		switch chunkType {
		case "VP8X":
			meta, err := parseWebPVP8X(chunk)
			if err != nil {
				return webPStickerMetadata{}, err
			}
			return meta, nil
		case "VP8L":
			meta, err := parseWebPVP8L(chunk)
			if err != nil {
				return webPStickerMetadata{}, err
			}
			return meta, nil
		case "VP8 ":
			meta, err := parseWebPVP8(chunk)
			if err != nil {
				return webPStickerMetadata{}, err
			}
			return meta, nil
		}
		off = chunkEnd
		if chunkSize%2 == 1 {
			off++
		}
	}
	return webPStickerMetadata{}, fmt.Errorf("missing WebP image chunk")
}

func parseWebPVP8X(chunk []byte) (webPStickerMetadata, error) {
	if len(chunk) < 10 {
		return webPStickerMetadata{}, fmt.Errorf("short VP8X chunk")
	}
	width := uint32(chunk[4]) | uint32(chunk[5])<<8 | uint32(chunk[6])<<16
	height := uint32(chunk[7]) | uint32(chunk[8])<<8 | uint32(chunk[9])<<16
	return webPStickerMetadata{
		width:    width + 1,
		height:   height + 1,
		animated: chunk[0]&0x02 != 0,
	}, nil
}

func parseWebPVP8L(chunk []byte) (webPStickerMetadata, error) {
	if len(chunk) < 5 || chunk[0] != 0x2f {
		return webPStickerMetadata{}, fmt.Errorf("invalid VP8L chunk")
	}
	bits := binary.LittleEndian.Uint32(chunk[1:5])
	return webPStickerMetadata{
		width:  (bits & 0x3fff) + 1,
		height: ((bits >> 14) & 0x3fff) + 1,
	}, nil
}

func parseWebPVP8(chunk []byte) (webPStickerMetadata, error) {
	if len(chunk) < 10 || !bytes.Equal(chunk[3:6], []byte{0x9d, 0x01, 0x2a}) {
		return webPStickerMetadata{}, fmt.Errorf("invalid VP8 chunk")
	}
	return webPStickerMetadata{
		width:  uint32(binary.LittleEndian.Uint16(chunk[6:8]) & 0x3fff),
		height: uint32(binary.LittleEndian.Uint16(chunk[8:10]) & 0x3fff),
	}, nil
}

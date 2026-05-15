package main

import (
	"context"
	"encoding/binary"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openclaw/wacli/internal/fsutil"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

func TestSendCommandIncludesStickerSubcommand(t *testing.T) {
	cmd := newSendCmd(&rootFlags{})
	for _, sub := range cmd.Commands() {
		if sub.Name() == "sticker" {
			return
		}
	}
	t.Fatalf("missing send sticker subcommand")
}

func TestSendStickerCommandExposesSharedSendFlags(t *testing.T) {
	cmd := newSendStickerCmd(&rootFlags{})
	for _, name := range []string{"to", "pick", "file", "reply-to", "reply-to-sender", "post-send-wait"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Fatalf("missing --%s flag", name)
		}
	}
}

func TestIsWebPStickerData(t *testing.T) {
	valid := testWebPVP8X(512, 512, false, nil)
	if !isWebPStickerData(valid) {
		t.Fatalf("valid WebP header was rejected")
	}
	for _, data := range [][]byte{
		nil,
		[]byte("RIFF\x10\x00\x00\x00PNG "),
		[]byte("not webp"),
	} {
		if isWebPStickerData(data) {
			t.Fatalf("invalid WebP header was accepted: %q", string(data))
		}
	}
}

func TestValidateWebPSticker(t *testing.T) {
	static := testWebPVP8X(512, 512, false, nil)
	meta, err := validateWebPSticker(static)
	if err != nil {
		t.Fatalf("validateWebPSticker: %v", err)
	}
	if meta.width != 512 || meta.height != 512 || meta.animated {
		t.Fatalf("metadata = %+v, want static 512x512", meta)
	}

	animated := testWebPVP8X(512, 512, true, bytesOfSize(101*1024))
	meta, err = validateWebPSticker(animated)
	if err != nil {
		t.Fatalf("animated sticker should allow >100 KiB: %v", err)
	}
	if !meta.animated {
		t.Fatalf("animated WebP was not detected")
	}

	for name, tc := range map[string]struct {
		data []byte
		want string
	}{
		"wrong dimensions":   {testWebPVP8X(256, 512, false, nil), "512x512"},
		"static too large":   {testWebPVP8X(512, 512, false, bytesOfSize(101*1024)), "static stickers"},
		"animated too large": {testWebPVP8X(512, 512, true, bytesOfSize(501*1024)), "animated stickers"},
	} {
		if _, err := validateWebPSticker(tc.data); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: expected %q error, got %v", name, tc.want, err)
		}
	}
}

func TestSendStickerRejectsNonWebPBeforeUpload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sticker.png")
	if err := fsutil.WritePrivateFile(path, []byte("not-webp")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, _, err := sendSticker(context.Background(), nil, types.JID{}, path, sendStickerOptions{})
	if err == nil || !strings.Contains(err.Error(), "stickers must be valid WebP") {
		t.Fatalf("expected WebP validation error, got %v", err)
	}
}

func TestNewStickerMessageAttachesUploadFieldsAndReply(t *testing.T) {
	up := whatsmeow.UploadResponse{
		URL:           "https://upload",
		DirectPath:    "/direct",
		MediaKey:      []byte("key"),
		FileEncSHA256: []byte("enc"),
		FileSHA256:    []byte("plain"),
		FileLength:    123,
	}
	meta := webPStickerMetadata{width: 512, height: 512, animated: true}
	info := &waProto.ContextInfo{
		StanzaID:    proto.String("quoted"),
		Participant: proto.String("15551234567@s.whatsapp.net"),
	}

	msg := newStickerMessage(up, info, meta)
	sticker := msg.GetStickerMessage()
	if sticker == nil {
		t.Fatalf("missing sticker message")
	}
	if sticker.GetMimetype() != sendStickerMIME {
		t.Fatalf("mime = %q, want %q", sticker.GetMimetype(), sendStickerMIME)
	}
	if sticker.GetURL() != up.URL || sticker.GetDirectPath() != up.DirectPath || sticker.GetFileLength() != up.FileLength {
		t.Fatalf("upload fields were not attached")
	}
	if string(sticker.GetMediaKey()) != string(up.MediaKey) ||
		string(sticker.GetFileSHA256()) != string(up.FileSHA256) ||
		string(sticker.GetFileEncSHA256()) != string(up.FileEncSHA256) {
		t.Fatalf("upload hashes were not attached")
	}
	if sticker.GetWidth() != meta.width || sticker.GetHeight() != meta.height || !sticker.GetIsAnimated() {
		t.Fatalf("sticker metadata was not attached")
	}
	if sticker.GetContextInfo() != info {
		t.Fatalf("reply context was not attached")
	}
}

func testWebPVP8X(width, height uint32, animated bool, extra []byte) []byte {
	chunk := make([]byte, 10)
	if animated {
		chunk[0] = 0x02
	}
	putUint24(chunk[4:7], width-1)
	putUint24(chunk[7:10], height-1)
	data := make([]byte, 0, 12+8+len(chunk)+len(extra))
	data = append(data, []byte("RIFF")...)
	data = binary.LittleEndian.AppendUint32(data, uint32(4+8+len(chunk)+len(extra)))
	data = append(data, []byte("WEBPVP8X")...)
	data = binary.LittleEndian.AppendUint32(data, uint32(len(chunk)))
	data = append(data, chunk...)
	data = append(data, extra...)
	return data
}

func putUint24(dst []byte, v uint32) {
	dst[0] = byte(v)
	dst[1] = byte(v >> 8)
	dst[2] = byte(v >> 16)
}

func bytesOfSize(n int) []byte {
	if n <= 0 {
		return nil
	}
	return make([]byte, n)
}

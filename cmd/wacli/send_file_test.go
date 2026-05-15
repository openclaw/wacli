package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openclaw/wacli/internal/fsutil"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"google.golang.org/protobuf/proto"
)

func TestDetectSendFileMIMEAddsOpusCodecForOgg(t *testing.T) {
	for _, tc := range []struct {
		name         string
		filePath     string
		mimeOverride string
		want         string
	}{
		{name: "extension", filePath: "voice.ogg", want: "audio/ogg; codecs=opus"},
		{name: "audio override", filePath: "voice.bin", mimeOverride: "audio/ogg", want: "audio/ogg; codecs=opus"},
		{name: "application override", filePath: "voice.bin", mimeOverride: "application/ogg", want: "audio/ogg; codecs=opus"},
		{name: "already has codec", filePath: "voice.bin", mimeOverride: "audio/ogg; codecs=opus", want: "audio/ogg; codecs=opus"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := detectSendFileMIME(tc.filePath, tc.mimeOverride, nil)
			if got != tc.want {
				t.Fatalf("mime = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestReadSendFileDataRejectsOversizedFile(t *testing.T) {
	path := t.TempDir() + "/huge.bin"
	if err := fsutil.WritePrivateFile(path, nil); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Truncate(path, maxSendFileSize+1); err != nil {
		t.Fatalf("Truncate: %v", err)
	}

	_, err := readSendFileData(path)
	if err == nil || !strings.Contains(err.Error(), "file too large") {
		t.Fatalf("expected file too large error, got %v", err)
	}
}

func TestSendFileCommandExposesReplyFlags(t *testing.T) {
	cmd := newSendFileCmd(&rootFlags{})
	for _, name := range []string{"reply-to", "reply-to-sender", "ptt"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Fatalf("missing --%s flag", name)
		}
	}
}

func TestSendVoiceCommandExposesSharedSendFlags(t *testing.T) {
	cmd := newSendVoiceCmd(&rootFlags{})
	for _, name := range []string{"to", "pick", "file", "mime", "reply-to", "reply-to-sender", "post-send-wait"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Fatalf("missing --%s flag", name)
		}
	}
}

func TestIsOggOpusMIME(t *testing.T) {
	for _, tc := range []struct {
		mime string
		want bool
	}{
		{mime: "audio/ogg; codecs=opus", want: true},
		{mime: "audio/ogg; codecs=\"opus\"", want: true},
		{mime: "audio/ogg", want: false},
		{mime: "audio/mpeg", want: false},
	} {
		if got := isOggOpusMIME(tc.mime); got != tc.want {
			t.Fatalf("isOggOpusMIME(%q) = %v, want %v", tc.mime, got, tc.want)
		}
	}
}

func TestNewAudioMessageAttachesPTTMetadata(t *testing.T) {
	up := whatsmeow.UploadResponse{
		URL:           "https://upload",
		DirectPath:    "/path",
		MediaKey:      []byte("key"),
		FileEncSHA256: []byte("enc"),
		FileSHA256:    []byte("plain"),
		FileLength:    123,
	}
	waveform := make([]byte, voiceWaveformSamples)
	for i := range waveform {
		waveform[i] = byte(i)
	}

	msg := newAudioMessage(up, "audio/ogg; codecs=opus", true, voiceNoteMetadata{seconds: 7, waveform: waveform})
	if !msg.GetPTT() {
		t.Fatalf("PTT = false, want true")
	}
	if msg.GetSeconds() != 7 {
		t.Fatalf("seconds = %d, want 7", msg.GetSeconds())
	}
	if string(msg.GetWaveform()) != string(waveform) {
		t.Fatalf("waveform not attached")
	}
}

func TestNewImageMessageAttachesDimensionsAndThumbnail(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 120, 60))
	for y := 0; y < 60; y++ {
		for x := 0; x < 120; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 120, A: 255})
		}
	}
	var data bytes.Buffer
	if err := png.Encode(&data, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}

	up := whatsmeow.UploadResponse{
		URL:           "https://upload",
		DirectPath:    "/path",
		MediaKey:      []byte("key"),
		FileEncSHA256: []byte("enc"),
		FileSHA256:    []byte("plain"),
		FileLength:    uint64(data.Len()),
	}
	msg, err := newImageMessage(up, "image/png", "caption", data.Bytes())
	if err != nil {
		t.Fatalf("newImageMessage: %v", err)
	}
	if msg.GetWidth() != 120 || msg.GetHeight() != 60 {
		t.Fatalf("dimensions = %dx%d, want 120x60", msg.GetWidth(), msg.GetHeight())
	}
	if msg.GetCaption() != "caption" {
		t.Fatalf("caption = %q", msg.GetCaption())
	}
	if len(msg.GetJPEGThumbnail()) == 0 {
		t.Fatalf("missing JPEG thumbnail")
	}
	if _, err := jpeg.Decode(bytes.NewReader(msg.GetJPEGThumbnail())); err != nil {
		t.Fatalf("thumbnail is not JPEG: %v", err)
	}
}

func TestNewImageMessageRejectsInvalidImageData(t *testing.T) {
	_, err := newImageMessage(whatsmeow.UploadResponse{}, "image/png", "", []byte("not an image"))
	if err == nil || !strings.Contains(err.Error(), "invalid image data") {
		t.Fatalf("expected invalid image error, got %v", err)
	}
}

func TestScaledDimensions(t *testing.T) {
	for _, tc := range []struct {
		width, height int
		wantW, wantH  int
	}{
		{width: 120, height: 60, wantW: 96, wantH: 48},
		{width: 60, height: 120, wantW: 48, wantH: 96},
		{width: 40, height: 30, wantW: 40, wantH: 30},
		{width: 1, height: 1000, wantW: 1, wantH: 96},
	} {
		gotW, gotH := scaledDimensions(tc.width, tc.height, imageThumbnailMaxDimension)
		if gotW != tc.wantW || gotH != tc.wantH {
			t.Fatalf("scaledDimensions(%d,%d) = %dx%d, want %dx%d", tc.width, tc.height, gotW, gotH, tc.wantW, tc.wantH)
		}
	}
}

func TestWaveformFromPCM16LE(t *testing.T) {
	data := make([]byte, voiceWaveformSamples*4)
	for i := 0; i < voiceWaveformSamples*2; i++ {
		sample := int16(100)
		if i >= voiceWaveformSamples {
			sample = 1000
		}
		binary.LittleEndian.PutUint16(data[i*2:i*2+2], uint16(sample))
	}

	waveform := waveformFromPCM16LE(data)
	if len(waveform) != voiceWaveformSamples {
		t.Fatalf("waveform length = %d, want %d", len(waveform), voiceWaveformSamples)
	}
	if waveform[0] == 0 {
		t.Fatalf("first sample = 0, want non-zero")
	}
	if waveform[len(waveform)-1] != voiceWaveformMax {
		t.Fatalf("last sample = %d, want %d", waveform[len(waveform)-1], voiceWaveformMax)
	}
}

func TestProbeAudioMetadataWithFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not installed")
	}

	path := filepath.Join(t.TempDir(), "voice.ogg")
	err := exec.Command("ffmpeg",
		"-hide_banner",
		"-loglevel", "error",
		"-f", "lavfi",
		"-i", "sine=frequency=440:duration=0.7",
		"-c:a", "libopus",
		path,
	).Run()
	if err != nil {
		t.Skipf("ffmpeg could not generate Opus fixture: %v", err)
	}

	if seconds := probeAudioSeconds(context.Background(), path); seconds != 1 {
		t.Fatalf("seconds = %d, want 1", seconds)
	}
	waveform := probeAudioWaveform(context.Background(), path)
	if len(waveform) != voiceWaveformSamples {
		t.Fatalf("waveform length = %d, want %d", len(waveform), voiceWaveformSamples)
	}
	hasNonZero := false
	for _, sample := range waveform {
		if sample > 0 {
			hasNonZero = true
			break
		}
	}
	if !hasNonZero {
		t.Fatalf("waveform is all zero")
	}
}

func TestAttachSendFileReplyContext(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  *waProto.Message
		got  func(*waProto.Message) *waProto.ContextInfo
	}{
		{
			name: "image",
			msg:  &waProto.Message{ImageMessage: &waProto.ImageMessage{}},
			got:  func(msg *waProto.Message) *waProto.ContextInfo { return msg.GetImageMessage().GetContextInfo() },
		},
		{
			name: "video",
			msg:  &waProto.Message{VideoMessage: &waProto.VideoMessage{}},
			got:  func(msg *waProto.Message) *waProto.ContextInfo { return msg.GetVideoMessage().GetContextInfo() },
		},
		{
			name: "audio",
			msg:  &waProto.Message{AudioMessage: &waProto.AudioMessage{}},
			got:  func(msg *waProto.Message) *waProto.ContextInfo { return msg.GetAudioMessage().GetContextInfo() },
		},
		{
			name: "document",
			msg:  &waProto.Message{DocumentMessage: &waProto.DocumentMessage{}},
			got:  func(msg *waProto.Message) *waProto.ContextInfo { return msg.GetDocumentMessage().GetContextInfo() },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := &waProto.ContextInfo{
				StanzaID:    proto.String("quoted"),
				Participant: proto.String("15551234567@s.whatsapp.net"),
			}
			attachSendFileReplyContext(tc.msg, info)
			if tc.got(tc.msg) != info {
				t.Fatalf("context info was not attached")
			}
		})
	}
}

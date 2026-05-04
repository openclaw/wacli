package main

import (
	"os"
	"strings"
	"testing"

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
	if err := os.WriteFile(path, nil, 0o600); err != nil {
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
	for _, name := range []string{"reply-to", "reply-to-sender"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Fatalf("missing --%s flag", name)
		}
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

package main

import (
	"testing"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
)

func TestBuildStatusTextMessageUsesStatusStyling(t *testing.T) {
	font := int32(2)
	msg, err := buildStatusTextMessage("hello status", statusTextOptions{
		BackgroundColor: "#ff00aa",
		Font:            &font,
	})
	if err != nil {
		t.Fatalf("buildStatusTextMessage: %v", err)
	}

	ext := msg.GetExtendedTextMessage()
	if ext == nil {
		t.Fatalf("missing ExtendedTextMessage")
	}
	if ext.GetText() != "hello status" {
		t.Fatalf("text = %q", ext.GetText())
	}
	if ext.BackgroundArgb == nil || ext.GetBackgroundArgb() != 0xffff00aa {
		t.Fatalf("backgroundArgb = %#x", ext.GetBackgroundArgb())
	}
	if ext.Font == nil || ext.GetFont() != waProto.ExtendedTextMessage_FontType(font) {
		t.Fatalf("font = %v", ext.GetFont())
	}
}

func TestSendStatusCommandExposesMediaFlags(t *testing.T) {
	cmd := newSendStatusCmd(&rootFlags{})
	for _, name := range []string{"file", "mime", "message", "post-send-wait"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Fatalf("missing --%s flag", name)
		}
	}
}

func TestStatusBroadcastJIDConstant(t *testing.T) {
	if types.StatusBroadcastJID.String() != "status@broadcast" {
		t.Fatalf("status broadcast JID = %q", types.StatusBroadcastJID.String())
	}
}

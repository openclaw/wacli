package wa

import (
	"testing"
	"time"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"google.golang.org/protobuf/proto"
)

func historyMessage(msg *waProto.Message) *waProto.WebMessageInfo {
	return &waProto.WebMessageInfo{
		Key: &waProto.MessageKey{
			ID:        proto.String("wrapped-hist"),
			FromMe:    proto.Bool(false),
			RemoteJID: proto.String("123@s.whatsapp.net"),
		},
		MessageTimestamp: proto.Uint64(uint64(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC).Unix())),
		Message:          msg,
	}
}

// Live events are unwrapped by whatsmeow before they reach the parser; history
// messages are not. Each envelope used to leave the row as "(message)".
func TestParseHistoryMessageUnwrapsContentEnvelopes(t *testing.T) {
	text := func(body string) *waProto.Message {
		return &waProto.Message{ExtendedTextMessage: &waProto.ExtendedTextMessage{Text: proto.String(body)}}
	}
	cases := []struct {
		name string
		msg  *waProto.Message
	}{
		{"ephemeral", &waProto.Message{EphemeralMessage: &waProto.FutureProofMessage{Message: text("disappearing hello")}}},
		{"viewOnce", &waProto.Message{ViewOnceMessage: &waProto.FutureProofMessage{Message: text("disappearing hello")}}},
		{"viewOnceV2", &waProto.Message{ViewOnceMessageV2: &waProto.FutureProofMessage{Message: text("disappearing hello")}}},
		{"viewOnceV2Extension", &waProto.Message{ViewOnceMessageV2Extension: &waProto.FutureProofMessage{Message: text("disappearing hello")}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pm := ParseHistoryMessage("123@s.whatsapp.net", historyMessage(tc.msg))
			if pm.Text != "disappearing hello" {
				t.Fatalf("Text = %q, want wrapped text", pm.Text)
			}
			if pm.UnhandledPayload != "" {
				t.Fatalf("UnhandledPayload = %q, want empty", pm.UnhandledPayload)
			}
			if pm.ID != "wrapped-hist" || pm.Chat.String() != "123@s.whatsapp.net" {
				t.Fatalf("identity changed: id=%q chat=%q", pm.ID, pm.Chat.String())
			}
		})
	}
}

func TestParseHistoryMessageUnwrapsDocumentWithCaption(t *testing.T) {
	pm := ParseHistoryMessage("123@s.whatsapp.net", historyMessage(&waProto.Message{
		DocumentWithCaptionMessage: &waProto.FutureProofMessage{
			Message: &waProto.Message{
				DocumentMessage: &waProto.DocumentMessage{
					FileName:   proto.String("listino.pdf"),
					Caption:    proto.String("Ecco il listino"),
					Mimetype:   proto.String("application/pdf"),
					DirectPath: proto.String("/v/doc"),
					FileLength: proto.Uint64(2048),
				},
			},
		},
	}))
	if pm.Media == nil || pm.Media.Type != "document" {
		t.Fatalf("Media = %+v, want document", pm.Media)
	}
	if pm.Media.Filename != "listino.pdf" || pm.Media.DirectPath != "/v/doc" {
		t.Fatalf("unexpected document media: %+v", pm.Media)
	}
	if pm.Text != "Ecco il listino" {
		t.Fatalf("Text = %q, want caption", pm.Text)
	}
	if pm.UnhandledPayload != "" {
		t.Fatalf("UnhandledPayload = %q, want empty", pm.UnhandledPayload)
	}
}

// Unwrapping must not drop what the outer envelopes say: the destination of a
// message sent from the phone, and the reply context of the wrapped content.
func TestParseHistoryMessageUnwrapsEphemeralInsideDeviceSent(t *testing.T) {
	pm := ParseHistoryMessage("me@s.whatsapp.net", historyMessage(&waProto.Message{
		DeviceSentMessage: &waProto.DeviceSentMessage{
			DestinationJID: proto.String("15551234567@s.whatsapp.net"),
			Message: &waProto.Message{
				EphemeralMessage: &waProto.FutureProofMessage{
					Message: &waProto.Message{
						ExtendedTextMessage: &waProto.ExtendedTextMessage{
							Text: proto.String("sent from phone"),
							ContextInfo: &waProto.ContextInfo{
								StanzaID:      proto.String("quoted-id"),
								Participant:   proto.String("15551234567@s.whatsapp.net"),
								QuotedMessage: &waProto.Message{Conversation: proto.String("original")},
							},
						},
					},
				},
			},
		},
	}))
	if !pm.FromMe {
		t.Fatalf("FromMe = false, want true")
	}
	if pm.Chat.String() != "15551234567@s.whatsapp.net" {
		t.Fatalf("Chat = %q, want destination", pm.Chat.String())
	}
	if pm.Text != "sent from phone" {
		t.Fatalf("Text = %q", pm.Text)
	}
	if pm.ReplyToID != "quoted-id" || pm.ReplyToDisplay != "original" {
		t.Fatalf("reply context = %q/%q", pm.ReplyToID, pm.ReplyToDisplay)
	}
}

func TestParseHistoryMessageUnwrapsEditInsideEphemeral(t *testing.T) {
	pm := ParseHistoryMessage("123@s.whatsapp.net", historyMessage(&waProto.Message{
		EphemeralMessage: &waProto.FutureProofMessage{
			Message: &waProto.Message{
				EditedMessage: &waProto.FutureProofMessage{
					Message: &waProto.Message{Conversation: proto.String("edited body")},
				},
			},
		},
	}))
	if !pm.Edited {
		t.Fatalf("Edited = false, want true")
	}
	if pm.Text != "edited body" {
		t.Fatalf("Text = %q", pm.Text)
	}
}

// The diagnostic should still name the leaf a wrapper hides, not the wrapper.
func TestParseHistoryMessageNamesLeafInsideEphemeral(t *testing.T) {
	pm := ParseHistoryMessage("123@s.whatsapp.net", historyMessage(&waProto.Message{
		EphemeralMessage: &waProto.FutureProofMessage{
			Message: &waProto.Message{
				StickerSyncRmrMessage: &waProto.StickerSyncRMRMessage{Filehash: []string{"abc"}},
			},
		},
	}))
	if pm.UnhandledPayload != "stickerSyncRmrMessage" {
		t.Fatalf("UnhandledPayload = %q, want stickerSyncRmrMessage", pm.UnhandledPayload)
	}
}

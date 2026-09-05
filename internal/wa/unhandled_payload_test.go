package wa

import (
	"testing"
	"time"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func liveEvent(msg *waProto.Message) *events.Message {
	chat, _ := types.ParseJID("123@s.whatsapp.net")
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat},
			ID:            "MSGID",
			Timestamp:     time.Unix(1, 0),
		},
		Message: msg,
	}
}

// A payload the parser understands must not be flagged, otherwise the warning
// becomes noise and gets ignored.
func TestParseLiveMessageLeavesUnhandledPayloadEmptyForText(t *testing.T) {
	pm := ParseLiveMessage(liveEvent(&waProto.Message{
		Conversation: proto.String("hello"),
	}))
	if pm.Text != "hello" {
		t.Fatalf("expected text to be extracted, got %q", pm.Text)
	}
	if pm.UnhandledPayload != "" {
		t.Fatalf("expected no unhandled payload, got %q", pm.UnhandledPayload)
	}
}

// A payload the parser extracts nothing from is stored as "(message)". Naming
// the field is what makes that row diagnosable after the fact.
func TestParseLiveMessageNamesUnextractedPayload(t *testing.T) {
	pm := ParseLiveMessage(liveEvent(&waProto.Message{
		StickerSyncRmrMessage: &waProto.StickerSyncRMRMessage{
			Filehash: []string{"abc"},
		},
	}))
	if pm.Text != "" || pm.Media != nil {
		t.Fatalf("expected no content, got text=%q media=%v", pm.Text, pm.Media)
	}
	if pm.UnhandledPayload != "stickerSyncRmrMessage" {
		t.Fatalf("expected stickerSyncRmrMessage, got %q", pm.UnhandledPayload)
	}
}

// messageContextInfo travels alongside real payloads and carries no content of
// its own, so reporting it would point at the wrong field.
func TestMarkUnhandledPayloadIgnoresMessageContextInfo(t *testing.T) {
	pm := ParseLiveMessage(liveEvent(&waProto.Message{
		MessageContextInfo: &waProto.MessageContextInfo{
			MessageSecret: []byte{1, 2, 3},
		},
	}))
	if pm.UnhandledPayload != "" {
		t.Fatalf("expected context-info-only message to be ignored, got %q", pm.UnhandledPayload)
	}
}

// An empty message has nothing to report; flagging it would fire on every
// keep-alive-shaped payload.
func TestMarkUnhandledPayloadIgnoresEmptyMessage(t *testing.T) {
	pm := ParseLiveMessage(liveEvent(&waProto.Message{}))
	if pm.UnhandledPayload != "" {
		t.Fatalf("expected empty message to be ignored, got %q", pm.UnhandledPayload)
	}
}

// History messages reach the same placeholder path, so they need the same
// diagnostic.
func TestParseHistoryMessageNamesUnextractedPayload(t *testing.T) {
	pm := ParseHistoryMessage("123@s.whatsapp.net", &waProto.WebMessageInfo{
		Key:              &waProto.MessageKey{ID: proto.String("HIST")},
		MessageTimestamp: proto.Uint64(1),
		Message: &waProto.Message{
			StickerSyncRmrMessage: &waProto.StickerSyncRMRMessage{
				Filehash: []string{"abc"},
			},
		},
	})
	if pm.UnhandledPayload != "stickerSyncRmrMessage" {
		t.Fatalf("expected stickerSyncRmrMessage, got %q", pm.UnhandledPayload)
	}
}

// A revoke produces no text by design; treating it as unhandled would flag
// every deletion.
func TestMarkUnhandledPayloadIgnoresRevoke(t *testing.T) {
	pm := ParseLiveMessage(liveEvent(&waProto.Message{
		ProtocolMessage: &waProto.ProtocolMessage{
			Type: waProto.ProtocolMessage_REVOKE.Enum(),
			Key:  &waProto.MessageKey{ID: proto.String("ORIGINAL")},
		},
	}))
	if !pm.Revoked {
		t.Fatal("expected message to be marked revoked")
	}
	if pm.UnhandledPayload != "" {
		t.Fatalf("expected revoke to be handled, got %q", pm.UnhandledPayload)
	}
}

// A payload wrapped in deviceSentMessage must be named by its leaf. Reporting
// the wrapper would send an operator looking at the envelope instead of the
// content that went missing.
func TestMarkUnhandledPayloadNamesLeafInsideDeviceSent(t *testing.T) {
	pm := ParseLiveMessage(liveEvent(&waProto.Message{
		DeviceSentMessage: &waProto.DeviceSentMessage{
			DestinationJID: proto.String("456@s.whatsapp.net"),
			Message: &waProto.Message{
				StickerSyncRmrMessage: &waProto.StickerSyncRMRMessage{
					Filehash: []string{"abc"},
				},
			},
		},
	}))
	if pm.UnhandledPayload != "stickerSyncRmrMessage" {
		t.Fatalf("expected the leaf payload, got %q", pm.UnhandledPayload)
	}
}

// The edit case is the reason this diagnostic exists: naming protocolMessage
// here would report every unhandled edit as the same generic wrapper.
func TestMarkUnhandledPayloadNamesLeafInsideProtocolEdit(t *testing.T) {
	pm := ParseLiveMessage(liveEvent(&waProto.Message{
		ProtocolMessage: &waProto.ProtocolMessage{
			Type: waProto.ProtocolMessage_MESSAGE_EDIT.Enum(),
			Key:  &waProto.MessageKey{ID: proto.String("ORIGINAL")},
			EditedMessage: &waProto.Message{
				StickerSyncRmrMessage: &waProto.StickerSyncRMRMessage{
					Filehash: []string{"abc"},
				},
			},
		},
	}))
	if pm.ID != "ORIGINAL" {
		t.Fatalf("expected the edit to adopt the original id, got %q", pm.ID)
	}
	if pm.UnhandledPayload != "stickerSyncRmrMessage" {
		t.Fatalf("expected the leaf payload, got %q", pm.UnhandledPayload)
	}
}

// editedMessage is the other wrapper shape the parser unwraps.
func TestMarkUnhandledPayloadNamesLeafInsideEditedMessage(t *testing.T) {
	pm := ParseLiveMessage(liveEvent(&waProto.Message{
		EditedMessage: &waProto.FutureProofMessage{
			Message: &waProto.Message{
				StickerSyncRmrMessage: &waProto.StickerSyncRMRMessage{
					Filehash: []string{"abc"},
				},
			},
		},
	}))
	if !pm.Edited {
		t.Fatal("expected the message to be marked edited")
	}
	if pm.UnhandledPayload != "stickerSyncRmrMessage" {
		t.Fatalf("expected the leaf payload, got %q", pm.UnhandledPayload)
	}
}

// A wrapper around content the parser understands stays unflagged.
func TestMarkUnhandledPayloadIgnoresWrappedText(t *testing.T) {
	pm := ParseLiveMessage(liveEvent(&waProto.Message{
		DeviceSentMessage: &waProto.DeviceSentMessage{
			Message: &waProto.Message{Conversation: proto.String("hello")},
		},
	}))
	if pm.Text != "hello" {
		t.Fatalf("expected wrapped text to be extracted, got %q", pm.Text)
	}
	if pm.UnhandledPayload != "" {
		t.Fatalf("expected no unhandled payload, got %q", pm.UnhandledPayload)
	}
}

func TestParseLiveMessageExtractsCommentMessage(t *testing.T) {
	pm := ParseLiveMessage(liveEvent(&waProto.Message{
		CommentMessage: &waE2E.CommentMessage{
			TargetMessageKey: &waCommon.MessageKey{
				ID: proto.String("TARGET-123"),
			},
			Message: &waProto.Message{
				Conversation: proto.String("this is a comment"),
			},
		},
	}))
	if pm.Text != "this is a comment" {
		t.Fatalf("expected comment text, got %q", pm.Text)
	}
	if pm.ReplyToID != "TARGET-123" {
		t.Fatalf("expected ReplyToID TARGET-123, got %q", pm.ReplyToID)
	}
	if pm.UnhandledPayload != "" {
		t.Fatalf("expected no unhandled payload, got %q", pm.UnhandledPayload)
	}
}

func TestParseLiveMessageExtractsAlbumMessage(t *testing.T) {
	pm := ParseLiveMessage(liveEvent(&waProto.Message{
		AlbumMessage: &waE2E.AlbumMessage{
			ExpectedImageCount: proto.Uint32(3),
			ExpectedVideoCount: proto.Uint32(1),
		},
	}))
	if pm.Text != "[Album: 3 images, 1 videos]" {
		t.Fatalf("expected album text, got %q", pm.Text)
	}
	if pm.UnhandledPayload != "" {
		t.Fatalf("expected no unhandled payload, got %q", pm.UnhandledPayload)
	}
}

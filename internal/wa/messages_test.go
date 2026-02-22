package wa

import (
	"testing"
	"time"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestParseHistoryMessageTextAndSender(t *testing.T) {
	h := &waProto.WebMessageInfo{
		Key: &waProto.MessageKey{
			ID:          proto.String("msgid"),
			FromMe:      proto.Bool(false),
			Participant: proto.String("sender@s.whatsapp.net"),
		},
		MessageTimestamp: proto.Uint64(uint64(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Unix())),
		Message:          &waProto.Message{Conversation: proto.String("hello")},
	}
	pm := ParseHistoryMessage("123@s.whatsapp.net", h)
	if pm.ID != "msgid" || pm.Text != "hello" {
		t.Fatalf("unexpected parsed msg: %+v", pm)
	}
	if pm.SenderJID != "sender@s.whatsapp.net" {
		t.Fatalf("unexpected sender: %q", pm.SenderJID)
	}
	if pm.Chat.String() != "123@s.whatsapp.net" {
		t.Fatalf("unexpected chat: %q", pm.Chat.String())
	}
}

func TestParseHistoryMessageGroupSenderFromTopLevelParticipant(t *testing.T) {
	h := &waProto.WebMessageInfo{
		Key: &waProto.MessageKey{
			ID:        proto.String("msgid"),
			FromMe:    proto.Bool(false),
			RemoteJID: proto.String("group@g.us"),
		},
		Participant:      proto.String("sender@s.whatsapp.net"),
		PushName:         proto.String("Alice"),
		MessageTimestamp: proto.Uint64(uint64(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Unix())),
		Message:          &waProto.Message{Conversation: proto.String("hello from group")},
	}
	pm := ParseHistoryMessage("group@g.us", h)
	if pm.SenderJID != "sender@s.whatsapp.net" {
		t.Fatalf("expected sender from top-level Participant, got %q", pm.SenderJID)
	}
	if pm.PushName != "Alice" {
		t.Fatalf("expected PushName 'Alice', got %q", pm.PushName)
	}
}

func TestParseHistoryMessagePushNameExtracted(t *testing.T) {
	h := &waProto.WebMessageInfo{
		Key: &waProto.MessageKey{
			ID:          proto.String("msgid"),
			FromMe:      proto.Bool(false),
			Participant: proto.String("sender@s.whatsapp.net"),
		},
		PushName:         proto.String("Bob"),
		MessageTimestamp: proto.Uint64(uint64(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Unix())),
		Message:          &waProto.Message{Conversation: proto.String("hello")},
	}
	pm := ParseHistoryMessage("group@g.us", h)
	if pm.PushName != "Bob" {
		t.Fatalf("expected PushName 'Bob', got %q", pm.PushName)
	}
}

func TestParseLiveMessageImageClonesBytes(t *testing.T) {
	chat, _ := types.ParseJID("123@s.whatsapp.net")
	sender, _ := types.ParseJID("sender@s.whatsapp.net")

	key := []byte{1, 2, 3}
	img := &waProto.ImageMessage{
		Caption:       proto.String("cap"),
		Mimetype:      proto.String("image/jpeg"),
		DirectPath:    proto.String("/direct"),
		MediaKey:      key,
		FileSHA256:    []byte{4},
		FileEncSHA256: []byte{5},
		FileLength:    proto.Uint64(10),
	}
	ev := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:     chat,
				Sender:   sender,
				IsFromMe: false,
			},
			ID:        "mid",
			Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			PushName:  "Sender",
		},
		Message: &waProto.Message{ImageMessage: img},
	}

	pm := ParseLiveMessage(ev)
	if pm.ID != "mid" || pm.Media == nil || pm.Media.Type != "image" {
		t.Fatalf("unexpected parsed: %+v", pm)
	}
	if pm.Text != "cap" {
		t.Fatalf("expected text from caption, got %q", pm.Text)
	}

	// Ensure clone() was used (pm.Media.MediaKey should not alias key).
	key[0] = 9
	if pm.Media.MediaKey[0] == 9 {
		t.Fatalf("expected MediaKey to be cloned")
	}
}

func TestParseLiveMessageReaction(t *testing.T) {
	chat, _ := types.ParseJID("123@s.whatsapp.net")
	sender, _ := types.ParseJID("sender@s.whatsapp.net")

	ev := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:     chat,
				Sender:   sender,
				IsFromMe: false,
			},
			ID:        "mid",
			Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			PushName:  "Sender",
		},
		Message: &waProto.Message{
			ReactionMessage: &waProto.ReactionMessage{
				Text: proto.String("👍"),
				Key:  &waProto.MessageKey{ID: proto.String("orig")},
			},
		},
	}

	pm := ParseLiveMessage(ev)
	if pm.ReactionEmoji != "👍" || pm.ReactionToID != "orig" {
		t.Fatalf("unexpected reaction parse: %+v", pm)
	}
}

func TestParseLiveMessageReply(t *testing.T) {
	chat, _ := types.ParseJID("123@s.whatsapp.net")
	sender, _ := types.ParseJID("sender@s.whatsapp.net")

	ev := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:     chat,
				Sender:   sender,
				IsFromMe: false,
			},
			ID:        "mid",
			Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			PushName:  "Sender",
		},
		Message: &waProto.Message{
			ExtendedTextMessage: &waProto.ExtendedTextMessage{
				Text: proto.String("reply text"),
				ContextInfo: &waProto.ContextInfo{
					StanzaID: proto.String("orig"),
					QuotedMessage: &waProto.Message{
						Conversation: proto.String("quoted"),
					},
				},
			},
		},
	}

	pm := ParseLiveMessage(ev)
	if pm.ReplyToID != "orig" {
		t.Fatalf("expected ReplyToID to be orig, got %q", pm.ReplyToID)
	}
	if pm.ReplyToDisplay != "quoted" {
		t.Fatalf("expected ReplyToDisplay to be quoted, got %q", pm.ReplyToDisplay)
	}
}

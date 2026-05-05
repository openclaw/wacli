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

func TestParseHistoryMessageTopLevelParticipant(t *testing.T) {
	groupJID := "120363001234567890@g.us"
	senderLID := "12345:67@lid"
	h := &waProto.WebMessageInfo{
		Key: &waProto.MessageKey{
			ID:        proto.String("msgid2"),
			FromMe:    proto.Bool(false),
			RemoteJID: proto.String(groupJID),
		},
		Participant:      proto.String(senderLID),
		MessageTimestamp: proto.Uint64(uint64(time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC).Unix())),
		Message:          &waProto.Message{Conversation: proto.String("from lid group")},
	}

	pm := ParseHistoryMessage(groupJID, h)
	if pm.SenderJID != senderLID {
		t.Fatalf("SenderJID = %q, want %q", pm.SenderJID, senderLID)
	}
}

func TestParseHistoryMessageKeyParticipantStillWorks(t *testing.T) {
	sender := "sender@s.whatsapp.net"
	h := &waProto.WebMessageInfo{
		Key: &waProto.MessageKey{
			ID:          proto.String("msgid3"),
			FromMe:      proto.Bool(false),
			RemoteJID:   proto.String("120363001234567890@g.us"),
			Participant: proto.String(sender),
		},
		MessageTimestamp: proto.Uint64(uint64(time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC).Unix())),
		Message:          &waProto.Message{Conversation: proto.String("from regular group")},
	}

	pm := ParseHistoryMessage("120363001234567890@g.us", h)
	if pm.SenderJID != sender {
		t.Fatalf("SenderJID = %q, want %q", pm.SenderJID, sender)
	}
}

func TestParseHistoryMessageStarred(t *testing.T) {
	starred := true
	h := &waProto.WebMessageInfo{
		Key: &waProto.MessageKey{
			ID:     proto.String("starred-msg"),
			FromMe: proto.Bool(false),
		},
		MessageTimestamp: proto.Uint64(uint64(time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC).Unix())),
		Message:          &waProto.Message{Conversation: proto.String("saved")},
		Starred:          &starred,
	}

	pm := ParseHistoryMessage("123@s.whatsapp.net", h)
	if !pm.StarredKnown || !pm.Starred {
		t.Fatalf("expected starred state, got %+v", pm)
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

func TestParseLiveMessageForwarded(t *testing.T) {
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
				Text: proto.String("forwarded text"),
				ContextInfo: &waProto.ContextInfo{
					IsForwarded:     proto.Bool(true),
					ForwardingScore: proto.Uint32(3),
				},
			},
		},
	}

	pm := ParseLiveMessage(ev)
	if !pm.IsForwarded {
		t.Fatalf("expected forwarded message, got %+v", pm)
	}
	if pm.ForwardingScore != 3 {
		t.Fatalf("ForwardingScore = %d, want 3", pm.ForwardingScore)
	}
}

func TestParseTemplateMessage(t *testing.T) {
	chat, _ := types.ParseJID("123@s.whatsapp.net")
	sender, _ := types.ParseJID("biz@s.whatsapp.net")

	ev := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat: chat, Sender: sender, IsFromMe: false,
			},
			ID:        "tmpl1",
			Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		Message: &waProto.Message{
			TemplateMessage: &waProto.TemplateMessage{
				HydratedTemplate: &waProto.TemplateMessage_HydratedFourRowTemplate{
					HydratedContentText: proto.String("Your appointment is confirmed"),
					HydratedFooterText:  proto.String("Reply STOP to opt out"),
				},
			},
		},
	}

	pm := ParseLiveMessage(ev)
	if pm.Text != "Your appointment is confirmed\n[Reply STOP to opt out]" {
		t.Fatalf("unexpected template text: %q", pm.Text)
	}
}

func TestParseButtonsMessage(t *testing.T) {
	chat, _ := types.ParseJID("123@s.whatsapp.net")
	sender, _ := types.ParseJID("biz@s.whatsapp.net")

	ev := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat: chat, Sender: sender, IsFromMe: false,
			},
			ID:        "btn1",
			Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		Message: &waProto.Message{
			ButtonsMessage: &waProto.ButtonsMessage{
				ContentText: proto.String("Pick an option"),
				FooterText:  proto.String("Powered by Biz"),
			},
		},
	}

	pm := ParseLiveMessage(ev)
	if pm.Text != "Pick an option\n[Powered by Biz]" {
		t.Fatalf("unexpected buttons text: %q", pm.Text)
	}
}

func TestParseButtonsResponseMessage(t *testing.T) {
	chat, _ := types.ParseJID("123@s.whatsapp.net")
	sender, _ := types.ParseJID("user@s.whatsapp.net")

	ev := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat: chat, Sender: sender, IsFromMe: true,
			},
			ID:        "btnresp1",
			Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		Message: &waProto.Message{
			ButtonsResponseMessage: &waProto.ButtonsResponseMessage{
				Response: &waProto.ButtonsResponseMessage_SelectedDisplayText{
					SelectedDisplayText: "Option A",
				},
			},
		},
	}

	pm := ParseLiveMessage(ev)
	if pm.Text != "Option A" {
		t.Fatalf("unexpected buttons response text: %q", pm.Text)
	}
}

func TestParseInteractiveMessage(t *testing.T) {
	chat, _ := types.ParseJID("123@s.whatsapp.net")
	sender, _ := types.ParseJID("biz@s.whatsapp.net")

	ev := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat: chat, Sender: sender, IsFromMe: false,
			},
			ID:        "interactive1",
			Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		Message: &waProto.Message{
			InteractiveMessage: &waProto.InteractiveMessage{
				Header: &waProto.InteractiveMessage_Header{
					Title:    proto.String("Welcome"),
					Subtitle: proto.String("sub"),
				},
				Body: &waProto.InteractiveMessage_Body{
					Text: proto.String("Browse our catalog"),
				},
				Footer: &waProto.InteractiveMessage_Footer{
					Text: proto.String("Terms apply"),
				},
			},
		},
	}

	pm := ParseLiveMessage(ev)
	if pm.Text != "Welcome\nBrowse our catalog\n[Terms apply]" {
		t.Fatalf("unexpected interactive text: %q", pm.Text)
	}
}

func TestParseListMessage(t *testing.T) {
	chat, _ := types.ParseJID("123@s.whatsapp.net")
	sender, _ := types.ParseJID("biz@s.whatsapp.net")

	ev := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat: chat, Sender: sender, IsFromMe: false,
			},
			ID:        "list1",
			Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		Message: &waProto.Message{
			ListMessage: &waProto.ListMessage{
				Title:       proto.String("Menu"),
				Description: proto.String("Choose an item"),
			},
		},
	}

	pm := ParseLiveMessage(ev)
	if pm.Text != "Menu\nChoose an item" {
		t.Fatalf("unexpected list text: %q", pm.Text)
	}
}

func TestParseListResponseMessage(t *testing.T) {
	chat, _ := types.ParseJID("123@s.whatsapp.net")
	sender, _ := types.ParseJID("user@s.whatsapp.net")

	ev := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat: chat, Sender: sender, IsFromMe: true,
			},
			ID:        "listresp1",
			Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		Message: &waProto.Message{
			ListResponseMessage: &waProto.ListResponseMessage{
				Title: proto.String("Item B"),
			},
		},
	}

	pm := ParseLiveMessage(ev)
	if pm.Text != "Item B" {
		t.Fatalf("unexpected list response text: %q", pm.Text)
	}
}

func TestParseTemplateButtonReplyMessage(t *testing.T) {
	chat, _ := types.ParseJID("123@s.whatsapp.net")
	sender, _ := types.ParseJID("user@s.whatsapp.net")

	ev := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat: chat, Sender: sender, IsFromMe: true,
			},
			ID:        "tbreply1",
			Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		Message: &waProto.Message{
			TemplateButtonReplyMessage: &waProto.TemplateButtonReplyMessage{
				SelectedDisplayText: proto.String("Book now"),
			},
		},
	}

	pm := ParseLiveMessage(ev)
	if pm.Text != "Book now" {
		t.Fatalf("unexpected template button reply text: %q", pm.Text)
	}
}

func TestDisplayTextForProtoBusinessTypes(t *testing.T) {
	tests := []struct {
		name string
		msg  *waProto.Message
		want string
	}{
		{
			name: "template",
			msg: &waProto.Message{
				TemplateMessage: &waProto.TemplateMessage{
					HydratedTemplate: &waProto.TemplateMessage_HydratedFourRowTemplate{
						HydratedContentText: proto.String("body text"),
					},
				},
			},
			want: "body text",
		},
		{
			name: "buttons",
			msg: &waProto.Message{
				ButtonsMessage: &waProto.ButtonsMessage{
					ContentText: proto.String("pick one"),
				},
			},
			want: "pick one",
		},
		{
			name: "interactive",
			msg: &waProto.Message{
				InteractiveMessage: &waProto.InteractiveMessage{
					Body: &waProto.InteractiveMessage_Body{Text: proto.String("shop here")},
				},
			},
			want: "shop here",
		},
		{
			name: "list",
			msg: &waProto.Message{
				ListMessage: &waProto.ListMessage{
					Description: proto.String("choose"),
				},
			},
			want: "choose",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := displayTextForProto(tc.msg)
			if got != tc.want {
				t.Fatalf("displayTextForProto(%s) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

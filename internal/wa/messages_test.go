package wa

import (
	"testing"
	"time"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waE2E"
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

func TestParseHistoryMessageCallLog(t *testing.T) {
	outcome := waProto.CallLogMessage_CONNECTED
	callType := waProto.CallLogMessage_REGULAR
	h := &waProto.WebMessageInfo{
		Key: &waProto.MessageKey{
			ID:     proto.String("call-msg"),
			FromMe: proto.Bool(true),
		},
		MessageTimestamp: proto.Uint64(uint64(time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC).Unix())),
		Message: &waProto.Message{
			CallLogMesssage: &waProto.CallLogMessage{
				IsVideo:      proto.Bool(true),
				CallOutcome:  &outcome,
				DurationSecs: proto.Int64(125),
				CallType:     &callType,
				Participants: []*waProto.CallLogMessage_CallParticipant{{
					JID:         proto.String("456@s.whatsapp.net"),
					CallOutcome: &outcome,
				}},
			},
		},
	}

	pm := ParseHistoryMessage("456@s.whatsapp.net", h)
	if pm.Call == nil {
		t.Fatal("expected call log metadata")
	}
	if pm.Call.EventType != "call_log" || pm.Call.CallID != "call-msg" || pm.Call.MsgID != "call-msg" {
		t.Fatalf("unexpected call identity: %+v", pm.Call)
	}
	if pm.Call.Direction != "outbound" || pm.Call.Media != "video" || pm.Call.Outcome != "connected" || pm.Call.DurationSecs != 125 {
		t.Fatalf("unexpected call details: %+v", pm.Call)
	}
	if len(pm.Call.Participants) != 1 || pm.Call.Participants[0].JID != "456@s.whatsapp.net" {
		t.Fatalf("unexpected participants: %+v", pm.Call.Participants)
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

func TestParseHistoryMessageUnwrapsProtocolEdit(t *testing.T) {
	groupJID := "120363001234567890@g.us"
	sender := "16048339070@s.whatsapp.net"
	h := &waProto.WebMessageInfo{
		Key: &waProto.MessageKey{
			ID:          proto.String("edit-event-id"),
			FromMe:      proto.Bool(false),
			RemoteJID:   proto.String(groupJID),
			Participant: proto.String(sender),
		},
		MessageTimestamp: proto.Uint64(uint64(time.Date(2026, 5, 15, 19, 30, 25, 0, time.UTC).Unix())),
		Message: &waProto.Message{
			ProtocolMessage: &waProto.ProtocolMessage{
				Type: waProto.ProtocolMessage_MESSAGE_EDIT.Enum(),
				Key: &waProto.MessageKey{
					ID:          proto.String("original-msg-id"),
					FromMe:      proto.Bool(false),
					RemoteJID:   proto.String(groupJID),
					Participant: proto.String(sender),
				},
				EditedMessage: &waProto.Message{Conversation: proto.String("edited body")},
			},
		},
	}

	pm := ParseHistoryMessage(groupJID, h)
	if pm.ID != "original-msg-id" {
		t.Fatalf("ID = %q, want original-msg-id", pm.ID)
	}
	if pm.Text != "edited body" {
		t.Fatalf("Text = %q, want edited body", pm.Text)
	}
	if !pm.Edited {
		t.Fatalf("Edited = false, want true")
	}
	if pm.SenderJID != sender {
		t.Fatalf("SenderJID = %q, want %q", pm.SenderJID, sender)
	}
	if pm.Chat.String() != groupJID {
		t.Fatalf("Chat = %q, want %q", pm.Chat.String(), groupJID)
	}
}

func TestParseHistoryMessageUnwrapsTopLevelEdit(t *testing.T) {
	groupJID := "120363001234567890@g.us"
	sender := "16048339070@s.whatsapp.net"
	h := &waProto.WebMessageInfo{
		Key: &waProto.MessageKey{
			ID:          proto.String("original-msg-id"),
			FromMe:      proto.Bool(false),
			RemoteJID:   proto.String(groupJID),
			Participant: proto.String(sender),
		},
		MessageTimestamp: proto.Uint64(uint64(time.Date(2026, 5, 15, 19, 37, 1, 0, time.UTC).Unix())),
		Message: &waProto.Message{
			EditedMessage: &waProto.FutureProofMessage{
				Message: &waProto.Message{Conversation: proto.String("top-level edited body")},
			},
		},
	}

	pm := ParseHistoryMessage(groupJID, h)
	if pm.ID != "original-msg-id" {
		t.Fatalf("ID = %q, want original-msg-id", pm.ID)
	}
	if pm.Text != "top-level edited body" {
		t.Fatalf("Text = %q, want top-level edited body", pm.Text)
	}
	if !pm.Edited {
		t.Fatalf("Edited = false, want true")
	}
	if pm.SenderJID != sender {
		t.Fatalf("SenderJID = %q, want %q", pm.SenderJID, sender)
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

func TestParseLiveMessagePollReplyAndForwardedContext(t *testing.T) {
	chat, _ := types.ParseJID("123@s.whatsapp.net")
	sender, _ := types.ParseJID("sender@s.whatsapp.net")

	ev := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:     chat,
				Sender:   sender,
				IsFromMe: false,
			},
			ID:        "poll-mid",
			Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			PushName:  "Sender",
		},
		Message: &waProto.Message{
			PollCreationMessageV3: &waProto.PollCreationMessage{
				Name: proto.String("Lunch?"),
				Options: []*waProto.PollCreationMessage_Option{
					{OptionName: proto.String("Pizza")},
					{OptionName: proto.String("Sushi")},
				},
				ContextInfo: &waProto.ContextInfo{
					StanzaID: proto.String("orig"),
					QuotedMessage: &waProto.Message{
						Conversation: proto.String("quoted text"),
					},
					IsForwarded:     proto.Bool(true),
					ForwardingScore: proto.Uint32(2),
				},
			},
		},
	}

	pm := ParseLiveMessage(ev)
	if pm.Poll == nil {
		t.Fatalf("expected poll parse, got %+v", pm)
	}
	if pm.ReplyToID != "orig" {
		t.Fatalf("expected ReplyToID to be orig, got %q", pm.ReplyToID)
	}
	if pm.ReplyToDisplay != "quoted text" {
		t.Fatalf("expected ReplyToDisplay to be quoted text, got %q", pm.ReplyToDisplay)
	}
	if !pm.IsForwarded || pm.ForwardingScore != 2 {
		t.Fatalf("expected forwarded poll context, got forwarded=%v score=%d", pm.IsForwarded, pm.ForwardingScore)
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

func TestParseContactMessageText(t *testing.T) {
	chat, _ := types.ParseJID("123@s.whatsapp.net")
	sender, _ := types.ParseJID("sender@s.whatsapp.net")

	ev := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:     chat,
				Sender:   sender,
				IsFromMe: false,
			},
			ID:        "contact1",
			Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		Message: &waProto.Message{
			ContactMessage: &waProto.ContactMessage{
				DisplayName: proto.String("Ada Lovelace"),
				Vcard: proto.String("BEGIN:VCARD\nVERSION:3.0\nFN:Ada Lovelace\n" +
					"TEL;type=CELL;waid=441234567890:+44 1234 567890\nEND:VCARD"),
			},
		},
	}

	pm := ParseLiveMessage(ev)
	if pm.Text != "Contact: Ada Lovelace (+44 1234 567890)" {
		t.Fatalf("unexpected contact text: %q", pm.Text)
	}
}

func TestParseContactsArrayMessageText(t *testing.T) {
	chat, _ := types.ParseJID("123@s.whatsapp.net")
	sender, _ := types.ParseJID("sender@s.whatsapp.net")

	ev := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:     chat,
				Sender:   sender,
				IsFromMe: false,
			},
			ID:        "contacts1",
			Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		Message: &waProto.Message{
			ContactsArrayMessage: &waProto.ContactsArrayMessage{
				DisplayName: proto.String("2 contacts"),
				Contacts: []*waProto.ContactMessage{
					{
						DisplayName: proto.String("Ada Lovelace"),
						Vcard:       proto.String("BEGIN:VCARD\nFN:Ada Lovelace\nTEL:+44 1234\nEND:VCARD"),
					},
					{
						DisplayName: proto.String("Grace Hopper"),
						Vcard:       proto.String("BEGIN:VCARD\nFN:Grace Hopper\nTEL:+1 555\nEND:VCARD"),
					},
				},
			},
		},
	}

	pm := ParseLiveMessage(ev)
	want := "Contacts:\nContact: Ada Lovelace (+44 1234)\nContact: Grace Hopper (+1 555)"
	if pm.Text != want {
		t.Fatalf("unexpected contacts text: %q", pm.Text)
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
	if len(pm.Buttons) != 0 {
		t.Fatalf("expected no buttons, got %d", len(pm.Buttons))
	}
}

func TestParseTemplateMessageWithURLButtons(t *testing.T) {
	chat, _ := types.ParseJID("123@s.whatsapp.net")
	sender, _ := types.ParseJID("biz@s.whatsapp.net")

	ev := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat: chat, Sender: sender, IsFromMe: false,
			},
			ID:        "tmpl2",
			Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		Message: &waProto.Message{
			TemplateMessage: &waProto.TemplateMessage{
				HydratedTemplate: &waProto.TemplateMessage_HydratedFourRowTemplate{
					HydratedContentText: proto.String("Check out our deals"),
					HydratedFooterText:  proto.String("Terms apply"),
					HydratedButtons: []*waProto.HydratedTemplateButton{
						{
							HydratedButton: &waProto.HydratedTemplateButton_UrlButton{
								UrlButton: &waProto.HydratedTemplateButton_HydratedURLButton{
									DisplayText: proto.String("Buy flights"),
									URL:         proto.String("https://example.com/flights"),
								},
							},
						},
						{
							HydratedButton: &waProto.HydratedTemplateButton_UrlButton{
								UrlButton: &waProto.HydratedTemplateButton_HydratedURLButton{
									DisplayText: proto.String("Buy packages"),
									URL:         proto.String("https://example.com/packages"),
								},
							},
						},
					},
				},
			},
		},
	}

	pm := ParseLiveMessage(ev)
	if pm.Text != "Check out our deals\n[Terms apply]" {
		t.Fatalf("unexpected template text: %q", pm.Text)
	}
	if len(pm.Buttons) != 2 {
		t.Fatalf("expected 2 buttons, got %d", len(pm.Buttons))
	}
	if pm.Buttons[0].Type != "url" || pm.Buttons[0].DisplayText != "Buy flights" || pm.Buttons[0].URL != "https://example.com/flights" {
		t.Fatalf("unexpected button[0]: %+v", pm.Buttons[0])
	}
	if pm.Buttons[1].Type != "url" || pm.Buttons[1].DisplayText != "Buy packages" || pm.Buttons[1].URL != "https://example.com/packages" {
		t.Fatalf("unexpected button[1]: %+v", pm.Buttons[1])
	}
}

func TestParseTemplateMessageWithMixedButtons(t *testing.T) {
	chat, _ := types.ParseJID("123@s.whatsapp.net")
	sender, _ := types.ParseJID("biz@s.whatsapp.net")

	ev := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat: chat, Sender: sender, IsFromMe: false,
			},
			ID:        "tmpl3",
			Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		Message: &waProto.Message{
			TemplateMessage: &waProto.TemplateMessage{
				HydratedTemplate: &waProto.TemplateMessage_HydratedFourRowTemplate{
					HydratedContentText: proto.String("Contact us"),
					HydratedButtons: []*waProto.HydratedTemplateButton{
						{
							HydratedButton: &waProto.HydratedTemplateButton_QuickReplyButton{
								QuickReplyButton: &waProto.HydratedTemplateButton_HydratedQuickReplyButton{
									DisplayText: proto.String("Yes"),
									ID:          proto.String("yes_id"),
								},
							},
						},
						{
							HydratedButton: &waProto.HydratedTemplateButton_CallButton{
								CallButton: &waProto.HydratedTemplateButton_HydratedCallButton{
									DisplayText: proto.String("Call us"),
									PhoneNumber: proto.String("+1234567890"),
								},
							},
						},
					},
				},
			},
		},
	}

	pm := ParseLiveMessage(ev)
	if len(pm.Buttons) != 2 {
		t.Fatalf("expected 2 buttons, got %d", len(pm.Buttons))
	}
	if pm.Buttons[0].Type != "quick_reply" || pm.Buttons[0].DisplayText != "Yes" || pm.Buttons[0].ID != "yes_id" {
		t.Fatalf("unexpected quick_reply button: %+v", pm.Buttons[0])
	}
	if pm.Buttons[1].Type != "call" || pm.Buttons[1].DisplayText != "Call us" || pm.Buttons[1].PhoneNumber != "+1234567890" {
		t.Fatalf("unexpected call button: %+v", pm.Buttons[1])
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
				Buttons: []*waProto.ButtonsMessage_Button{
					{
						ButtonID: proto.String("btn_a"),
						ButtonText: &waProto.ButtonsMessage_Button_ButtonText{
							DisplayText: proto.String("Option A"),
						},
					},
					{
						ButtonID: proto.String("btn_b"),
						ButtonText: &waProto.ButtonsMessage_Button_ButtonText{
							DisplayText: proto.String("Option B"),
						},
					},
				},
			},
		},
	}

	pm := ParseLiveMessage(ev)
	if pm.Text != "Pick an option\n[Powered by Biz]" {
		t.Fatalf("unexpected buttons text: %q", pm.Text)
	}
	if len(pm.Buttons) != 2 {
		t.Fatalf("expected 2 buttons, got %d", len(pm.Buttons))
	}
	if pm.Buttons[0].Type != "quick_reply" || pm.Buttons[0].DisplayText != "Option A" || pm.Buttons[0].ID != "btn_a" {
		t.Fatalf("unexpected button[0]: %+v", pm.Buttons[0])
	}
	if pm.Buttons[1].Type != "quick_reply" || pm.Buttons[1].DisplayText != "Option B" || pm.Buttons[1].ID != "btn_b" {
		t.Fatalf("unexpected button[1]: %+v", pm.Buttons[1])
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

func TestParseInteractiveMessageWithNativeFlowButtons(t *testing.T) {
	chat, _ := types.ParseJID("123@s.whatsapp.net")
	sender, _ := types.ParseJID("biz@s.whatsapp.net")

	ev := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat: chat, Sender: sender, IsFromMe: false,
			},
			ID:        "native1",
			Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		Message: &waProto.Message{
			InteractiveMessage: &waProto.InteractiveMessage{
				Body: &waProto.InteractiveMessage_Body{
					Text: proto.String("Complete your payment"),
				},
				InteractiveMessage: &waProto.InteractiveMessage_NativeFlowMessage_{
					NativeFlowMessage: &waProto.InteractiveMessage_NativeFlowMessage{
						Buttons: []*waProto.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
							{
								Name:             proto.String("cta_url"),
								ButtonParamsJSON: proto.String(`{"display_text":"Pay now","url":"https://pay.example.com"}`),
							},
							{
								Name:             proto.String("quick_reply"),
								ButtonParamsJSON: proto.String(`{"display_text":"Cancel","id":"cancel"}`),
							},
							{
								Name:             proto.String("cta_call"),
								ButtonParamsJSON: proto.String(`{"display_text":"Call","phone_number":"+15551234567"}`),
							},
							{
								Name:             proto.String("quick_reply"),
								ButtonParamsJSON: proto.String(`{`),
							},
						},
					},
				},
			},
		},
	}

	pm := ParseLiveMessage(ev)
	if pm.Text != "Complete your payment" {
		t.Fatalf("unexpected text: %q", pm.Text)
	}
	if len(pm.Buttons) != 3 {
		t.Fatalf("expected 3 buttons, got %d: %+v", len(pm.Buttons), pm.Buttons)
	}
	if pm.Buttons[0].Type != "url" || pm.Buttons[0].DisplayText != "Pay now" || pm.Buttons[0].URL != "https://pay.example.com" {
		t.Fatalf("unexpected button[0]: %+v", pm.Buttons[0])
	}
	if pm.Buttons[1].Type != "quick_reply" || pm.Buttons[1].DisplayText != "Cancel" || pm.Buttons[1].ID != "cancel" {
		t.Fatalf("unexpected button[1]: %+v", pm.Buttons[1])
	}
	if pm.Buttons[2].Type != "call" || pm.Buttons[2].DisplayText != "Call" || pm.Buttons[2].PhoneNumber != "+15551234567" {
		t.Fatalf("unexpected button[2]: %+v", pm.Buttons[2])
	}
}

func TestParseInteractiveTemplateWithNativeFlowButtons(t *testing.T) {
	chat, _ := types.ParseJID("123@s.whatsapp.net")
	sender, _ := types.ParseJID("biz@s.whatsapp.net")

	ev := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat: chat, Sender: sender, IsFromMe: false,
			},
			ID:        "native-template1",
			Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		Message: &waProto.Message{
			TemplateMessage: &waProto.TemplateMessage{
				Format: &waProto.TemplateMessage_InteractiveMessageTemplate{
					InteractiveMessageTemplate: &waProto.InteractiveMessage{
						Body: &waProto.InteractiveMessage_Body{
							Text: proto.String("Choose an option"),
						},
						InteractiveMessage: &waProto.InteractiveMessage_NativeFlowMessage_{
							NativeFlowMessage: &waProto.InteractiveMessage_NativeFlowMessage{
								Buttons: []*waProto.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
									{
										Name:             proto.String("cta_url"),
										ButtonParamsJSON: proto.String(`{"display_text":"Open","url":"https://example.com"}`),
									},
									{
										Name:             proto.String("quick_reply"),
										ButtonParamsJSON: proto.String(`{"display_text":"Reply","id":"reply"}`),
									},
								},
							},
						},
					},
				},
			},
		},
	}

	pm := ParseLiveMessage(ev)
	if pm.Text != "Choose an option" {
		t.Fatalf("unexpected text: %q", pm.Text)
	}
	if len(pm.Buttons) != 2 {
		t.Fatalf("expected 2 buttons, got %d: %+v", len(pm.Buttons), pm.Buttons)
	}
	if pm.Buttons[0].Type != "url" || pm.Buttons[0].DisplayText != "Open" || pm.Buttons[0].URL != "https://example.com" {
		t.Fatalf("unexpected button[0]: %+v", pm.Buttons[0])
	}
	if pm.Buttons[1].Type != "quick_reply" || pm.Buttons[1].DisplayText != "Reply" || pm.Buttons[1].ID != "reply" {
		t.Fatalf("unexpected button[1]: %+v", pm.Buttons[1])
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
				ButtonText:  proto.String("Options"),
				Sections: []*waProto.ListMessage_Section{
					{
						Title: proto.String("Section 1"),
						Rows: []*waProto.ListMessage_Row{
							{Title: proto.String("Alice"), RowID: proto.String("alice"), Description: proto.String("Send to Alice")},
							{Title: proto.String("Bob"), RowID: proto.String("bob")},
						},
					},
				},
			},
		},
	}

	pm := ParseLiveMessage(ev)
	if pm.Text != "Menu\nChoose an item" {
		t.Fatalf("unexpected list text: %q", pm.Text)
	}
	if len(pm.Buttons) != 3 {
		t.Fatalf("expected 3 buttons (1 list + 2 rows), got %d", len(pm.Buttons))
	}
	if pm.Buttons[0].Type != "list" || pm.Buttons[0].DisplayText != "Options" {
		t.Fatalf("unexpected list button: %+v", pm.Buttons[0])
	}
	if pm.Buttons[1].Type != "list_row" || pm.Buttons[1].DisplayText != "Alice" || pm.Buttons[1].ID != "alice" || pm.Buttons[1].Description != "Send to Alice" {
		t.Fatalf("unexpected row[0]: %+v", pm.Buttons[1])
	}
	if pm.Buttons[2].Type != "list_row" || pm.Buttons[2].DisplayText != "Bob" || pm.Buttons[2].ID != "bob" {
		t.Fatalf("unexpected row[1]: %+v", pm.Buttons[2])
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

func TestParseLiveMessageRevokeTargetsOriginalID(t *testing.T) {
	chat := types.NewJID("15551234567", types.DefaultUserServer)
	ev := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:     chat,
				Sender:   chat,
				IsFromMe: true,
			},
			ID:        "revoke-event",
			Timestamp: time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC),
		},
		Message: &waProto.Message{
			ProtocolMessage: &waProto.ProtocolMessage{
				Type: waProto.ProtocolMessage_REVOKE.Enum(),
				Key: &waProto.MessageKey{
					ID:        proto.String("original"),
					FromMe:    proto.Bool(true),
					RemoteJID: proto.String(chat.String()),
				},
			},
		},
	}

	pm := ParseLiveMessage(ev)
	if !pm.Revoked || pm.ID != "original" || pm.Chat != chat || !pm.FromMe {
		t.Fatalf("unexpected revoked parse: %+v", pm)
	}
}

func TestDisplayTextForProtoBusinessTypes(t *testing.T) {
	tests := []struct {
		name string
		msg  *waProto.Message
		want string
	}{
		{
			name: "contact",
			msg: &waProto.Message{
				ContactMessage: &waProto.ContactMessage{
					DisplayName: proto.String("Ada Lovelace"),
					Vcard:       proto.String("BEGIN:VCARD\nFN:Ada Lovelace\nTEL:+44 1234\nEND:VCARD"),
				},
			},
			want: "Contact: Ada Lovelace (+44 1234)",
		},
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

func TestParseLiveMessagePollCreationV3(t *testing.T) {
	chat, _ := types.ParseJID("15551112222@s.whatsapp.net")
	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat},
			ID:            "POLL-1",
			Timestamp:     time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC),
		},
		Message: &waProto.Message{
			PollCreationMessageV3: &waProto.PollCreationMessage{
				Name: proto.String("Pizza?"),
				Options: []*waProto.PollCreationMessage_Option{
					{OptionName: proto.String("Yes")},
					{OptionName: proto.String("No")},
					{OptionName: proto.String("Maybe")},
				},
				SelectableOptionsCount: proto.Uint32(2),
			},
		},
	}
	pm := ParseLiveMessage(evt)
	if pm.Poll == nil {
		t.Fatalf("expected Poll set, got nil; pm=%+v", pm)
	}
	if pm.Poll.Question != "Pizza?" {
		t.Fatalf("question = %q", pm.Poll.Question)
	}
	if got, want := pm.Poll.Options, []string{"Yes", "No", "Maybe"}; !equalStrings(got, want) {
		t.Fatalf("options = %v want %v", got, want)
	}
	if pm.Poll.SelectableCount != 2 {
		t.Fatalf("selectable = %d", pm.Poll.SelectableCount)
	}
	if pm.Text != "Poll: Pizza?" {
		t.Fatalf("text = %q", pm.Text)
	}
}

func TestParseLiveMessagePollCreationV1(t *testing.T) {
	chat, _ := types.ParseJID("15551112222@s.whatsapp.net")
	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat},
			ID:            "POLL-V1",
			Timestamp:     time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC),
		},
		Message: &waProto.Message{
			PollCreationMessage: &waProto.PollCreationMessage{
				Name:    proto.String("Hello?"),
				Options: []*waProto.PollCreationMessage_Option{{OptionName: proto.String("a")}, {OptionName: proto.String("b")}},
			},
		},
	}
	pm := ParseLiveMessage(evt)
	if pm.Poll == nil || pm.Poll.Question != "Hello?" {
		t.Fatalf("v1 poll not parsed: %+v", pm)
	}
}

func TestParseLiveMessagePollCreationV6(t *testing.T) {
	chat, _ := types.ParseJID("15551112222@s.whatsapp.net")
	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat},
			ID:            "POLL-V6",
			Timestamp:     time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC),
		},
		Message: &waProto.Message{
			PollCreationMessageV6: &waProto.PollCreationMessage{
				Name:    proto.String("V6?"),
				Options: []*waProto.PollCreationMessage_Option{{OptionName: proto.String("a")}, {OptionName: proto.String("b")}},
			},
		},
	}
	pm := ParseLiveMessage(evt)
	if pm.Poll == nil || pm.Poll.Question != "V6?" {
		t.Fatalf("v6 poll not parsed: %+v", pm)
	}
}

func TestParseLiveMessagePollUpdateRefersToCreation(t *testing.T) {
	chat, _ := types.ParseJID("15551112222@s.whatsapp.net")
	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat},
			ID:            "VOTE-1",
			Timestamp:     time.Date(2026, 5, 9, 12, 5, 0, 0, time.UTC),
		},
		Message: &waProto.Message{
			PollUpdateMessage: &waProto.PollUpdateMessage{
				PollCreationMessageKey: &waProto.MessageKey{
					ID:          proto.String("POLL-1"),
					RemoteJID:   proto.String("15551112222@s.whatsapp.net"),
					FromMe:      proto.Bool(true),
					Participant: proto.String("15551112222@s.whatsapp.net"),
				},
			},
		},
	}
	pm := ParseLiveMessage(evt)
	if pm.PollVote == nil {
		t.Fatalf("expected PollVote set, got nil; pm=%+v", pm)
	}
	if pm.PollVote.PollMessageID != "POLL-1" {
		t.Fatalf("poll msg id = %q", pm.PollVote.PollMessageID)
	}
	if pm.PollVote.PollChatJID != "15551112222@s.whatsapp.net" {
		t.Fatalf("poll chat jid = %q", pm.PollVote.PollChatJID)
	}
}

func TestParseLiveMessagePollAddOption(t *testing.T) {
	chat, _ := types.ParseJID("15551112222@s.whatsapp.net")
	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat},
			ID:            "ADD-1",
			Timestamp:     time.Date(2026, 5, 9, 12, 6, 0, 0, time.UTC),
		},
		Message: &waProto.Message{
			PollAddOptionMessage: &waE2E.PollAddOptionMessage{
				PollCreationMessageKey: &waProto.MessageKey{
					ID:        proto.String("POLL-1"),
					RemoteJID: proto.String("15551112222@s.whatsapp.net"),
				},
				AddOption: &waProto.PollCreationMessage_Option{OptionName: proto.String("Maybe")},
			},
		},
	}
	pm := ParseLiveMessage(evt)
	if pm.PollAdd == nil {
		t.Fatalf("expected PollAdd set, got nil; pm=%+v", pm)
	}
	if pm.PollAdd.PollMessageID != "POLL-1" || pm.PollAdd.Option != "Maybe" {
		t.Fatalf("poll add = %+v", pm.PollAdd)
	}
	if pm.Text != "Poll option added" {
		t.Fatalf("text = %q", pm.Text)
	}
}

func TestParseLiveMessageEncryptedPollAddOptionRef(t *testing.T) {
	chat, _ := types.ParseJID("15551112222@s.whatsapp.net")
	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat},
			ID:            "ADD-ENC",
			Timestamp:     time.Date(2026, 5, 9, 12, 6, 0, 0, time.UTC),
		},
		Message: &waProto.Message{
			SecretEncryptedMessage: &waE2E.SecretEncryptedMessage{
				TargetMessageKey: &waProto.MessageKey{
					ID:        proto.String("POLL-1"),
					RemoteJID: proto.String("15551112222@s.whatsapp.net"),
				},
				SecretEncType: waE2E.SecretEncryptedMessage_POLL_ADD_OPTION.Enum(),
			},
		},
	}
	pm := ParseLiveMessage(evt)
	if pm.PollAdd == nil {
		t.Fatalf("expected PollAdd set, got nil; pm=%+v", pm)
	}
	if pm.PollAdd.PollMessageID != "POLL-1" || pm.PollAdd.Option != "" {
		t.Fatalf("poll add = %+v", pm.PollAdd)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

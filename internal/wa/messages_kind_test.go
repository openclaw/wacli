package wa

import (
	"testing"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types/events"
)

func TestSemanticKindsDoNotPretendToBeMedia(t *testing.T) {
	for _, tc := range []struct {
		kind string
		msg  *waProto.Message
	}{
		{"poll", &waProto.Message{PollCreationMessage: &waProto.PollCreationMessage{}}},
		{"poll_vote", &waProto.Message{PollUpdateMessage: &waProto.PollUpdateMessage{}}},
		{"contact", &waProto.Message{ContactMessage: &waProto.ContactMessage{}}},
		{"contacts", &waProto.Message{ContactsArrayMessage: &waProto.ContactsArrayMessage{}}},
		{"event", &waProto.Message{EventMessage: &waProto.EventMessage{}}},
		{"", &waProto.Message{}},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			live := ParseLiveMessage(&events.Message{Message: tc.msg})
			history := ParseHistoryMessage("123@s.whatsapp.net", &waProto.WebMessageInfo{Message: tc.msg})
			for _, parsed := range []ParsedMessage{live, history} {
				if parsed.Kind != tc.kind || parsed.Media != nil {
					t.Fatalf("%+v", parsed)
				}
				if tc.kind != "" && !parsed.HasContent() {
					t.Fatal("semantic message lost")
				}
			}
		})
	}
}

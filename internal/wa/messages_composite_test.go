package wa

import (
	"testing"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func TestCommentEnvelopeReplyContext(t *testing.T) {
	for _, history := range []bool{false, true} {
		for _, target := range []string{"OUTER", "INNER", " "} {
			m := &waProto.Message{CommentMessage: &waE2E.CommentMessage{
				TargetMessageKey: &waCommon.MessageKey{ID: proto.String(target), Participant: proto.String("15555550124@s.whatsapp.net")},
				Message: &waProto.Message{ExtendedTextMessage: &waProto.ExtendedTextMessage{
					Text:        proto.String("comment body"),
					ContextInfo: &waProto.ContextInfo{StanzaID: proto.String("INNER"), Participant: proto.String("15555550125@s.whatsapp.net"), QuotedMessage: &waProto.Message{Conversation: proto.String("inner quote")}, IsForwarded: proto.Bool(true)},
				}},
			}}
			pm := ParseLiveMessage(liveEvent(m))
			if history {
				pm = ParseHistoryMessage("15555550123@s.whatsapp.net", &waProto.WebMessageInfo{Message: m})
			}
			wantID, wantSender, wantQuote := target, "15555550124@s.whatsapp.net", "inner quote"
			if target == "OUTER" {
				wantQuote = ""
			} else if target == " " {
				wantID, wantSender = "INNER", "15555550125@s.whatsapp.net"
			}
			if pm.Text != "comment body" || pm.ReplyToID != wantID || pm.ReplyToSenderJID != wantSender || pm.ReplyToDisplay != wantQuote || !pm.IsForwarded || pm.UnhandledPayload != "" {
				t.Fatalf("history=%v target=%q: text=%q reply=%q sender=%q quote=%q forwarded=%v unhandled=%q", history, target, pm.Text, pm.ReplyToID, pm.ReplyToSenderJID, pm.ReplyToDisplay, pm.IsForwarded, pm.UnhandledPayload)
			}
		}
	}
}

func TestAlbumSummariesAndContext(t *testing.T) {
	for _, tc := range []struct {
		images, videos uint32
		want           string
	}{{0, 0, "[Album]"}, {2, 0, "[Album: 2 images]"}, {0, 2, "[Album: 2 videos]"}, {2, 3, "[Album: 2 images, 3 videos]"}} {
		m := &waProto.Message{AlbumMessage: &waE2E.AlbumMessage{ExpectedImageCount: proto.Uint32(tc.images), ExpectedVideoCount: proto.Uint32(tc.videos), ContextInfo: &waProto.ContextInfo{StanzaID: proto.String("ALBUM-PARENT")}}}
		for _, pm := range []ParsedMessage{ParseLiveMessage(liveEvent(m)), ParseHistoryMessage("15555550123@s.whatsapp.net", &waProto.WebMessageInfo{Message: m})} {
			if pm.Text != tc.want || pm.ReplyToID != "ALBUM-PARENT" || pm.UnhandledPayload != "" {
				t.Fatalf("album: text=%q reply=%q unhandled=%q", pm.Text, pm.ReplyToID, pm.UnhandledPayload)
			}
		}
	}
}

func TestCommentKeepsUnhandledLeafDiagnostic(t *testing.T) {
	m := &waProto.Message{CommentMessage: &waE2E.CommentMessage{Message: &waProto.Message{StickerSyncRmrMessage: &waProto.StickerSyncRMRMessage{Filehash: []string{"synthetic"}}}}}
	if got := ParseLiveMessage(liveEvent(m)).UnhandledPayload; got != "stickerSyncRmrMessage" {
		t.Fatalf("unhandled leaf = %q", got)
	}
	if got := ParseLiveMessage(liveEvent(&waProto.Message{CommentMessage: &waE2E.CommentMessage{}})).UnhandledPayload; got != "commentMessage" {
		t.Fatalf("empty comment diagnostic = %q", got)
	}
}

func TestAssociatedAndGroupStatusContent(t *testing.T) {
	for name, wrap := range map[string]func(*waProto.Message) *waProto.Message{
		"associatedChildMessage": func(m *waProto.Message) *waProto.Message {
			return &waProto.Message{AssociatedChildMessage: &waE2E.FutureProofMessage{Message: m}}
		},
		"groupStatusMentionMessage": func(m *waProto.Message) *waProto.Message {
			return &waProto.Message{GroupStatusMentionMessage: &waE2E.FutureProofMessage{Message: m}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := &waProto.ContextInfo{StanzaID: proto.String("quoted-id"), Participant: proto.String("15555550124@s.whatsapp.net"), QuotedMessage: &waProto.Message{Conversation: proto.String("quoted text")}, IsForwarded: proto.Bool(true)}
			for _, media := range []bool{false, true} {
				inner := &waProto.Message{ExtendedTextMessage: &waProto.ExtendedTextMessage{Text: proto.String("searchable body"), ContextInfo: ctx}}
				if media {
					inner = &waProto.Message{ImageMessage: &waProto.ImageMessage{Caption: proto.String("searchable body"), ContextInfo: ctx, DirectPath: proto.String("/synthetic/media"), MediaKey: []byte{1, 2, 3}}}
				}
				m := wrap(wrap(inner))
				wantQuote := "searchable body"
				if media {
					wantQuote = "Sent image"
				}
				if got := displayTextForProto(m); got != wantQuote {
					t.Fatalf("wrapped quote=%q want %q", got, wantQuote)
				}
				live := liveEvent(m)
				live.Info.Sender = live.Info.Chat
				hist := &waProto.WebMessageInfo{Key: &waCommon.MessageKey{ID: proto.String(live.Info.ID), RemoteJID: proto.String(live.Info.Chat.String()), Participant: proto.String(live.Info.Sender.String())}, Message: m}
				for _, pm := range []ParsedMessage{ParseLiveMessage(live), ParseHistoryMessage(live.Info.Chat.String(), hist)} {
					if pm.Text != "searchable body" || pm.ID != live.Info.ID || pm.Chat != live.Info.Chat || pm.SenderJID != live.Info.Sender.String() || pm.ReplyToID != "quoted-id" || pm.ReplyToDisplay != "quoted text" || !pm.IsForwarded || pm.UnhandledPayload != "" {
						t.Fatalf("media=%t: %+v", media, pm)
					}
					if media && (pm.Media == nil || pm.Media.Type != "image" || pm.Media.DirectPath != "/synthetic/media") {
						t.Fatalf("lost wrapped media: %+v", pm.Media)
					}
				}
			}
			if got := ParseLiveMessage(liveEvent(wrap(&waProto.Message{StickerSyncRmrMessage: &waProto.StickerSyncRMRMessage{Filehash: []string{"synthetic"}}}))).UnhandledPayload; got != "stickerSyncRmrMessage" {
				t.Fatalf("leaf diagnostic=%q", got)
			}
			if got := ParseLiveMessage(liveEvent(wrap(nil))).UnhandledPayload; got != name {
				t.Fatalf("empty wrapper diagnostic=%q", got)
			}
		})
	}
}

func TestGroupInviteContentAndContext(t *testing.T) {
	for _, tc := range []struct{ caption, name, want string }{{"join this discussion", "Readers", "join this discussion"}, {"", "Readers", "Group invite: Readers"}, {"", "", "[Group invite]"}} {
		m := &waProto.Message{GroupInviteMessage: &waE2E.GroupInviteMessage{Caption: proto.String(tc.caption), GroupName: proto.String(tc.name), GroupJID: proto.String("120363000001@g.us"), InviteCode: proto.String("synthetic-invite-code"), ContextInfo: &waProto.ContextInfo{StanzaID: proto.String("invite-parent")}}}
		for _, pm := range []ParsedMessage{ParseLiveMessage(liveEvent(m)), ParseHistoryMessage("15555550123@s.whatsapp.net", &waProto.WebMessageInfo{Message: m})} {
			if pm.Text != tc.want || pm.ReplyToID != "invite-parent" || pm.UnhandledPayload != "" {
				t.Fatalf("invite content=%+v", pm)
			}
		}
		if got := displayTextForProto(m); got != tc.want {
			t.Fatalf("quoted invite=%q want %q", got, tc.want)
		}
	}
}

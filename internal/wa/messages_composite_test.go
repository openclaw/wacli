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

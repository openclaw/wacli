package wa

import (
	"bytes"
	"testing"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"google.golang.org/protobuf/proto"
)

func ptvVideo() *waProto.VideoMessage {
	return &waProto.VideoMessage{
		Mimetype:      proto.String("video/mp4"),
		DirectPath:    proto.String("/v/ptv"),
		MediaKey:      []byte{1, 2, 3},
		FileSHA256:    []byte{4, 5, 6},
		FileEncSHA256: []byte{7, 8, 9},
		FileLength:    proto.Uint64(4096),
		Seconds:       proto.Uint32(12),
	}
}

// Round video notes used to be stored as "(message)" with no media to fetch.
func TestParseLiveMessagePtvIsVideoMedia(t *testing.T) {
	ptv := ptvVideo()
	pm := ParseLiveMessage(liveEvent(&waProto.Message{PtvMessage: ptv}))
	if pm.Media == nil {
		t.Fatal("Media = nil, want video")
	}
	if pm.Media.Type != "video" || pm.Media.MimeType != "video/mp4" || pm.Media.DirectPath != "/v/ptv" || pm.Media.FileLength != 4096 {
		t.Fatalf("unexpected media: %+v", pm.Media)
	}
	if !bytes.Equal(pm.Media.MediaKey, []byte{1, 2, 3}) ||
		!bytes.Equal(pm.Media.FileSHA256, []byte{4, 5, 6}) ||
		!bytes.Equal(pm.Media.FileEncSHA256, []byte{7, 8, 9}) {
		t.Fatalf("unexpected media hashes: %+v", pm.Media)
	}
	ptv.MediaKey[0] = 99
	if pm.Media.MediaKey[0] != 1 {
		t.Fatal("MediaKey shares memory with the proto")
	}
	if !pm.HasContent() || pm.UnhandledPayload != "" {
		t.Fatalf("HasContent=%v UnhandledPayload=%q", pm.HasContent(), pm.UnhandledPayload)
	}
}

func TestParseHistoryMessagePtvInsideEphemeral(t *testing.T) {
	pm := ParseHistoryMessage("123@s.whatsapp.net", historyMessage(&waProto.Message{
		EphemeralMessage: &waProto.FutureProofMessage{
			Message: &waProto.Message{PtvMessage: ptvVideo()},
		},
	}))
	if pm.Media == nil || pm.Media.Type != "video" {
		t.Fatalf("Media = %+v, want video", pm.Media)
	}
}

func TestParseLiveMessagePtvKeepsReplyContext(t *testing.T) {
	ptv := ptvVideo()
	ptv.ContextInfo = &waProto.ContextInfo{
		StanzaID:      proto.String("quoted-id"),
		Participant:   proto.String("sender@s.whatsapp.net"),
		QuotedMessage: &waProto.Message{Conversation: proto.String("ciao")},
	}
	pm := ParseLiveMessage(liveEvent(&waProto.Message{PtvMessage: ptv}))
	if pm.ReplyToID != "quoted-id" || pm.ReplyToSenderJID != "sender@s.whatsapp.net" || pm.ReplyToDisplay != "ciao" {
		t.Fatalf("reply context = %q/%q/%q", pm.ReplyToID, pm.ReplyToSenderJID, pm.ReplyToDisplay)
	}
}

func TestDisplayTextForQuotedPtv(t *testing.T) {
	if got := displayTextForProto(&waProto.Message{PtvMessage: ptvVideo()}); got != "Sent video" {
		t.Fatalf("displayTextForProto = %q, want Sent video", got)
	}
}

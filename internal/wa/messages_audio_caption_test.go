package wa

import (
	"testing"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"google.golang.org/protobuf/proto"
)

// AudioMessage has no caption field in the protocol, so the only value the audio
// branch could put in Media.Caption is the "[Audio]" placeholder it just wrote to
// Text. Storing it reports content the sender never wrote on the one media type
// that can never carry a caption.
func TestParseLiveMessageLeavesAudioCaptionEmpty(t *testing.T) {
	pm := ParseLiveMessage(liveEvent(&waProto.Message{
		AudioMessage: &waProto.AudioMessage{
			Mimetype: proto.String("audio/ogg; codecs=opus"),
			PTT:      proto.Bool(true),
		},
	}))
	if pm.Media == nil {
		t.Fatal("expected audio media to be extracted")
	}
	if pm.Media.Type != "audio" {
		t.Fatalf("expected media type audio, got %q", pm.Media.Type)
	}
	if pm.Media.Caption != "" {
		t.Fatalf("expected an empty caption, got %q", pm.Media.Caption)
	}
	// The Text placeholder is the long-standing display behaviour and stays.
	if pm.Text != "[Audio]" {
		t.Fatalf("expected the [Audio] text placeholder, got %q", pm.Text)
	}
}

// A captioned image is the contrast case: there Caption is real user content and
// must survive, so the fix above must not be read as "captions are noise".
func TestParseLiveMessageKeepsImageCaption(t *testing.T) {
	pm := ParseLiveMessage(liveEvent(&waProto.Message{
		ImageMessage: &waProto.ImageMessage{
			Mimetype: proto.String("image/jpeg"),
			Caption:  proto.String("leak under the sink"),
		},
	}))
	if pm.Media == nil {
		t.Fatal("expected image media to be extracted")
	}
	if pm.Media.Caption != "leak under the sink" {
		t.Fatalf("expected the image caption to survive, got %q", pm.Media.Caption)
	}
}

// A message carrying real text alongside the audio payload keeps that text as
// the caption, exactly as before this change: only the synthesized placeholder
// is dropped, never a value the sender actually supplied.
func TestParseLiveMessageKeepsTextAccompanyingAudio(t *testing.T) {
	pm := ParseLiveMessage(liveEvent(&waProto.Message{
		Conversation: proto.String("the recording from this morning"),
		AudioMessage: &waProto.AudioMessage{
			Mimetype: proto.String("audio/ogg; codecs=opus"),
		},
	}))
	if pm.Media == nil {
		t.Fatal("expected audio media to be extracted")
	}
	if pm.Media.Caption != "the recording from this morning" {
		t.Fatalf("expected the accompanying text to survive as the caption, got %q", pm.Media.Caption)
	}
	if pm.Text != "the recording from this morning" {
		t.Fatalf("expected the text to be unchanged, got %q", pm.Text)
	}
}

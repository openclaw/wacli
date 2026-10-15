package wa

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestParseLiveMessageAdReferral(t *testing.T) {
	mediaType := waE2E.ContextInfo_ExternalAdReplyInfo_VIDEO
	chat, _ := types.ParseJID("123@s.whatsapp.net")
	ev := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat},
			ID:            "mid",
			Timestamp:     time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			PushName:      "Lead",
		},
		Message: &waProto.Message{
			ExtendedTextMessage: &waProto.ExtendedTextMessage{
				Text: proto.String("Hi, I saw your ad"),
				ContextInfo: &waProto.ContextInfo{
					ExternalAdReply: &waProto.ContextInfo_ExternalAdReplyInfo{
						Title:      proto.String("Train with a coach"),
						Body:       proto.String("Book a free call"),
						SourceType: proto.String("ad"),
						SourceID:   proto.String("120200000000000001"),
						SourceURL:  proto.String("https://fb.me/example"),
						SourceApp:  proto.String("instagram"),
						CtwaClid:   proto.String("clid-1"),
						MediaType:  &mediaType,
						Thumbnail:  []byte{0x1, 0x2},
					},
					ConversionSource:           proto.String("FB_Ads"),
					EntryPointConversionSource: proto.String("ctwa_ad"),
					EntryPointConversionApp:    proto.String("instagram"),
				},
			},
		},
	}

	want := AdReferral{
		SourceType:       "ad",
		SourceID:         "120200000000000001",
		SourceURL:        "https://fb.me/example",
		SourceApp:        "instagram",
		Title:            "Train with a coach",
		Body:             "Book a free call",
		MediaType:        "video",
		CtwaClid:         "clid-1",
		ConversionSource: "FB_Ads",
		EntryPointSource: "ctwa_ad",
		EntryPointApp:    "instagram",
	}
	pm := ParseLiveMessage(ev)
	if pm.AdReferral == nil || *pm.AdReferral != want {
		t.Fatalf("AdReferral = %+v, want %+v", pm.AdReferral, want)
	}
	if pm.Text != "Hi, I saw your ad" {
		t.Fatalf("Text = %q", pm.Text)
	}

	ev.Message.ExtendedTextMessage.ContextInfo = &waProto.ContextInfo{IsForwarded: proto.Bool(true)}
	if got := ParseLiveMessage(ev).AdReferral; got != nil {
		t.Fatalf("AdReferral without ad context = %+v, want nil", got)
	}
}

func TestParseLiveMessageAdReferralMediaTypeNoneIsOmitted(t *testing.T) {
	ref := adReferralFromContext(&waProto.ContextInfo{
		ExternalAdReply: &waE2E.ContextInfo_ExternalAdReplyInfo{SourceID: proto.String("1")},
	})
	if ref == nil || ref.MediaType != "" {
		t.Fatalf("AdReferral = %+v, want source only with empty media type", ref)
	}
	blob, err := json.Marshal(ref)
	if err != nil || strings.Contains(string(blob), "media_type") || strings.Contains(string(blob), "Thumbnail") {
		t.Fatalf("json = %s, err = %v", blob, err)
	}
}

func TestAdReferralFieldsAreCappedOnRuneBoundary(t *testing.T) {
	long := "x" + strings.Repeat("\u00e1", adReferralFieldLimit)
	ref := adReferralFromContext(&waProto.ContextInfo{
		ExternalAdReply: &waE2E.ContextInfo_ExternalAdReplyInfo{
			Title:     proto.String("  " + long),
			Body:      proto.String(long),
			SourceURL: proto.String(strings.Repeat("u", adReferralFieldLimit+10)),
		},
		ConversionSource: proto.String(long),
	})
	if ref == nil {
		t.Fatal("AdReferral = nil")
	}
	for name, got := range map[string]string{"title": ref.Title, "body": ref.Body, "conversion": ref.ConversionSource, "url": ref.SourceURL} {
		if len(got) == 0 || len(got) > adReferralFieldLimit || !utf8.ValidString(got) {
			t.Fatalf("%s len = %d valid = %v", name, len(got), utf8.ValidString(got))
		}
		if !strings.HasPrefix(long, got) && name != "url" {
			t.Fatalf("%s is not a prefix of the input", name)
		}
	}
	if len(ref.SourceURL) != adReferralFieldLimit {
		t.Fatalf("url len = %d, want %d", len(ref.SourceURL), adReferralFieldLimit)
	}
}

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
	mediaType := waE2E.ContextInfo_ExternalAdReplyInfo_NONE
	pm := ParseLiveMessage(&events.Message{
		Message: &waProto.Message{
			ExtendedTextMessage: &waProto.ExtendedTextMessage{
				Text: proto.String("Hi"),
				ContextInfo: &waProto.ContextInfo{
					ExternalAdReply: &waProto.ContextInfo_ExternalAdReplyInfo{
						SourceID:  proto.String("1"),
						MediaType: &mediaType,
						Thumbnail: make([]byte, 64*1024),
					},
				},
			},
		},
	})
	if pm.AdReferral == nil || *pm.AdReferral != (AdReferral{SourceID: "1"}) {
		t.Fatalf("AdReferral = %+v, want source only with empty media type", pm.AdReferral)
	}
	blob, err := json.Marshal(pm.AdReferral)
	if err != nil || len(blob) > 128 || strings.Contains(string(blob), "media_type") || strings.Contains(strings.ToLower(string(blob)), "thumbnail") {
		t.Fatalf("json = %s, err = %v", blob, err)
	}
}

func TestParseLiveMessageAdReferralFieldsAreCappedOnRuneBoundary(t *testing.T) {
	long := "x" + strings.Repeat("\u00e1", adReferralFieldLimit)
	url := strings.Repeat("u", adReferralFieldLimit+10)
	pm := ParseLiveMessage(&events.Message{
		Message: &waProto.Message{
			ExtendedTextMessage: &waProto.ExtendedTextMessage{
				Text: proto.String("Hi"),
				ContextInfo: &waProto.ContextInfo{
					ExternalAdReply: &waProto.ContextInfo_ExternalAdReplyInfo{
						Title:     proto.String("  " + long),
						Body:      proto.String(long),
						SourceURL: proto.String(url),
					},
					ConversionSource: proto.String(long),
				},
			},
		},
	})
	ref := pm.AdReferral
	if ref == nil {
		t.Fatal("AdReferral = nil")
	}
	for name, got := range map[string]string{"title": ref.Title, "body": ref.Body, "conversion_source": ref.ConversionSource, "url": ref.SourceURL} {
		if len(got) == 0 || len(got) > adReferralFieldLimit || !utf8.ValidString(got) {
			t.Fatalf("%s len = %d valid = %v", name, len(got), utf8.ValidString(got))
		}
		input, wantLen := long, adReferralFieldLimit-1
		if name == "url" {
			input, wantLen = url, adReferralFieldLimit
		}
		if !strings.HasPrefix(input, got) {
			t.Fatalf("%s is not a prefix of the input", name)
		}
		if len(got) != wantLen {
			t.Fatalf("%s len = %d, want %d", name, len(got), wantLen)
		}
	}
}

func TestParseHistoryMessageAdReferral(t *testing.T) {
	mediaType := waE2E.ContextInfo_ExternalAdReplyInfo_VIDEO
	chat, _ := types.ParseJID("123@s.whatsapp.net")
	message := &waProto.Message{
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
	pm := ParseHistoryMessage(chat.String(), &waProto.WebMessageInfo{
		Key: &waProto.MessageKey{
			ID:        proto.String("mid"),
			RemoteJID: proto.String(chat.String()),
			FromMe:    proto.Bool(false),
		},
		Message: message,
	})
	if pm.AdReferral == nil || *pm.AdReferral != want {
		t.Fatalf("AdReferral = %+v, want %+v", pm.AdReferral, want)
	}
	live := ParseLiveMessage(&events.Message{
		Info:    types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: chat}},
		Message: message,
	})
	if live.AdReferral == nil || *live.AdReferral != *pm.AdReferral {
		t.Fatalf("live AdReferral = %+v, history AdReferral = %+v", live.AdReferral, pm.AdReferral)
	}
}

func TestParseLiveMessageAdReferralContextShapes(t *testing.T) {
	tests := []struct {
		name    string
		message *waProto.Message
		fromMe  bool
		want    *AdReferral
	}{
		{
			name: "entry_point_only",
			message: &waProto.Message{ExtendedTextMessage: &waProto.ExtendedTextMessage{
				Text: proto.String("Hi"),
				ContextInfo: &waProto.ContextInfo{
					ConversionSource:           proto.String("FB_Ads"),
					EntryPointConversionSource: proto.String("ctwa_ad"),
					EntryPointConversionApp:    proto.String("instagram"),
				},
			}},
			want: &AdReferral{ConversionSource: "FB_Ads", EntryPointSource: "ctwa_ad", EntryPointApp: "instagram"},
		},
		{
			name: "from_me_image_conversion_source_only",
			message: &waProto.Message{ImageMessage: &waProto.ImageMessage{
				ContextInfo: &waProto.ContextInfo{ConversionSource: proto.String("FB_Ads")},
			}},
			fromMe: true,
			want:   &AdReferral{ConversionSource: "FB_Ads"},
		},
		{
			name:    "plain_conversation",
			message: &waProto.Message{Conversation: proto.String("Hi")},
		},
		{
			name: "organic_entry_point",
			message: &waProto.Message{ExtendedTextMessage: &waProto.ExtendedTextMessage{
				Text: proto.String("Hi"),
				ContextInfo: &waProto.ContextInfo{
					EntryPointConversionSource: proto.String("global_search_new_chat"),
					EntryPointConversionApp:    proto.String("whatsapp"),
				},
			}},
			want: &AdReferral{EntryPointSource: "global_search_new_chat", EntryPointApp: "whatsapp"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := ParseLiveMessage(&events.Message{
				Info:    types.MessageInfo{MessageSource: types.MessageSource{IsFromMe: tt.fromMe}},
				Message: tt.message,
			})
			if tt.want == nil {
				if pm.AdReferral != nil {
					t.Fatalf("AdReferral = %+v, want nil", pm.AdReferral)
				}
			} else if pm.AdReferral == nil || *pm.AdReferral != *tt.want {
				t.Fatalf("AdReferral = %+v, want %+v", pm.AdReferral, tt.want)
			}
			if pm.FromMe != tt.fromMe {
				t.Fatalf("FromMe = %v, want %v", pm.FromMe, tt.fromMe)
			}
		})
	}
}

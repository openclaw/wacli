package wa

import (
	"testing"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"google.golang.org/protobuf/proto"
)

func TestExtractLocationStaticPin(t *testing.T) {
	pm := &ParsedMessage{}
	extractLocation(&waProto.Message{
		LocationMessage: &waProto.LocationMessage{
			DegreesLatitude:  proto.Float64(51.4779),
			DegreesLongitude: proto.Float64(-0.0015),
			Name:             proto.String("Head office"),
			Address:          proto.String("1 Market Street"),
		},
	}, pm)

	if pm.Location == nil {
		t.Fatal("expected a parsed location")
	}
	if pm.Location.Latitude != 51.4779 || pm.Location.Longitude != -0.0015 {
		t.Fatalf("coordinates = %v,%v", pm.Location.Latitude, pm.Location.Longitude)
	}
	if pm.Location.Name != "Head office" || pm.Location.Address != "1 Market Street" {
		t.Fatalf("name = %q address = %q", pm.Location.Name, pm.Location.Address)
	}
	if pm.Location.IsLive {
		t.Fatal("a static pin must not be marked live")
	}
	if pm.Media == nil || pm.Media.Type != "location" {
		t.Fatalf("media = %+v", pm.Media)
	}
	if pm.Media.DirectPath != "" || len(pm.Media.MediaKey) != 0 {
		t.Fatal("location must not carry download metadata")
	}
}

func TestExtractLocationLiveShare(t *testing.T) {
	pm := &ParsedMessage{}
	extractLocation(&waProto.Message{
		LiveLocationMessage: &waProto.LiveLocationMessage{
			DegreesLatitude:  proto.Float64(51.5),
			DegreesLongitude: proto.Float64(-0.12),
			Caption:          proto.String("on my way"),
		},
	}, pm)

	if pm.Location == nil {
		t.Fatal("expected a parsed live location")
	}
	if !pm.Location.IsLive {
		t.Fatal("live location must be marked live")
	}
	if pm.Location.Name != "on my way" {
		t.Fatalf("name = %q", pm.Location.Name)
	}
	if pm.Media == nil || pm.Media.Type != "live_location" {
		t.Fatalf("media = %+v", pm.Media)
	}
}

func TestExtractLocationIgnoresOtherMessages(t *testing.T) {
	pm := &ParsedMessage{}
	extractLocation(&waProto.Message{Conversation: proto.String("hola")}, pm)

	if pm.Location != nil || pm.Media != nil {
		t.Fatal("a text message must not produce a location")
	}
}

func TestParseWAProtoKeepsLocationCoordinates(t *testing.T) {
	pm := &ParsedMessage{}
	extractWAProto(&waProto.Message{
		LocationMessage: &waProto.LocationMessage{
			DegreesLatitude:  proto.Float64(51.4779),
			DegreesLongitude: proto.Float64(-0.0015),
		},
	}, pm)

	if pm.Location == nil {
		t.Fatal("location dropped by the top-level parse path")
	}
	if pm.Location.Latitude != 51.4779 {
		t.Fatalf("latitude = %v want 51.4779", pm.Location.Latitude)
	}
}

func TestParseWAProtoKeepsLiveLocationReplyContext(t *testing.T) {
	pm := &ParsedMessage{}
	extractWAProto(&waProto.Message{
		LiveLocationMessage: &waProto.LiveLocationMessage{
			DegreesLatitude:  proto.Float64(51.4779),
			DegreesLongitude: proto.Float64(-0.0015),
			ContextInfo: &waProto.ContextInfo{
				StanzaID:    proto.String("QUOTED-1"),
				Participant: proto.String("15551234567@s.whatsapp.net"),
			},
		},
	}, pm)

	if pm.ReplyToID != "QUOTED-1" {
		t.Fatalf("reply id = %q", pm.ReplyToID)
	}
	if pm.ReplyToSenderJID != "15551234567@s.whatsapp.net" {
		t.Fatalf("reply sender = %q", pm.ReplyToSenderJID)
	}
}

func TestDisplayTextForProtoLabelsLiveLocation(t *testing.T) {
	got := displayTextForProto(&waProto.Message{
		LiveLocationMessage: &waProto.LiveLocationMessage{
			DegreesLatitude:  proto.Float64(51.4779),
			DegreesLongitude: proto.Float64(-0.0015),
		},
	})
	if got != "Sent live location" {
		t.Fatalf("display text = %q", got)
	}
}

func TestQuotedLiveLocationRendersALabel(t *testing.T) {
	pm := &ParsedMessage{}
	extractWAProto(&waProto.Message{
		ExtendedTextMessage: &waProto.ExtendedTextMessage{
			Text: proto.String("on my way too"),
			ContextInfo: &waProto.ContextInfo{
				StanzaID: proto.String("QUOTED-1"),
				QuotedMessage: &waProto.Message{
					LiveLocationMessage: &waProto.LiveLocationMessage{
						DegreesLatitude:  proto.Float64(51.4779),
						DegreesLongitude: proto.Float64(-0.0015),
					},
				},
			},
		},
	}, pm)

	if pm.ReplyToDisplay != "Sent live location" {
		t.Fatalf("quoted display = %q", pm.ReplyToDisplay)
	}
}

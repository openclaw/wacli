package wa

import (
	"testing"
	"time"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// The parsed timestamp is marshalled straight into webhook payloads, so its
// location decides the wire format. whatsmeow hands the event time over in the
// host's zone, which would make one message serialize differently per machine.
func TestParseLiveMessageNormalizesTimestampToUTC(t *testing.T) {
	zone := time.FixedZone("test-0400", -4*60*60)
	local := time.Date(2026, 9, 4, 14, 57, 52, 0, zone)
	chat, _ := types.ParseJID("15551234567@s.whatsapp.net")

	pm := ParseLiveMessage(&events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat},
			ID:            "MSGID",
			Timestamp:     local,
		},
		Message: &waProto.Message{Conversation: proto.String("hi")},
	})

	if pm.Timestamp.Location() != time.UTC {
		t.Fatalf("timestamp location = %v, want UTC", pm.Timestamp.Location())
	}
	// Same instant, different spelling: normalizing must not shift the clock.
	if !pm.Timestamp.Equal(local) {
		t.Fatalf("timestamp = %s, want the same instant as %s", pm.Timestamp, local)
	}
	if got := pm.Timestamp.Format(time.RFC3339); got != "2026-09-04T18:57:52Z" {
		t.Fatalf("serialized timestamp = %s, want 2026-09-04T18:57:52Z", got)
	}
}

// ParseHistoryMessage already normalized; pinned so the two paths cannot drift
// apart again.
func TestParseHistoryMessageTimestampIsUTC(t *testing.T) {
	pm := ParseHistoryMessage("15551234567@s.whatsapp.net", &waProto.WebMessageInfo{
		Key:              &waProto.MessageKey{ID: proto.String("HIST")},
		MessageTimestamp: proto.Uint64(1788548272),
		Message:          &waProto.Message{Conversation: proto.String("hi")},
	})
	if pm.Timestamp.Location() != time.UTC {
		t.Fatalf("timestamp location = %v, want UTC", pm.Timestamp.Location())
	}
}

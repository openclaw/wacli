package wa

import (
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

func TestCallLogRecordTimestampUnits(t *testing.T) {
	want := time.Date(2026, 5, 22, 10, 45, 0, 0, time.UTC)
	for _, raw := range []int64{want.Unix(), want.UnixMilli(), want.UnixMicro()} {
		if got := callLogRecordTimestamp(raw, time.Time{}); !got.Equal(want) {
			t.Fatalf("timestamp %d = %s, want %s", raw, got, want)
		}
	}

	fallback := time.Date(2026, 5, 22, 11, 0, 0, 0, time.UTC)
	if got := callLogRecordTimestamp(0, fallback); !got.Equal(fallback) {
		t.Fatalf("fallback = %s, want %s", got, fallback)
	}
}

func TestCallLogRecordChatIgnoresSelfGroupJIDForOneToOne(t *testing.T) {
	self := types.NewJID("15550000001", types.DefaultUserServer)
	peer := types.NewJID("15550000002", types.DefaultUserServer)
	call, ok := ParseCallLogRecord(&waSyncAction.CallLogRecord{
		CallID:         proto.String("call-1"),
		CallCreatorJID: proto.String(self.String()),
		GroupJID:       proto.String(self.String()),
		Participants: []*waSyncAction.CallLogRecord_ParticipantInfo{{
			UserJID: proto.String(peer.String()),
		}},
		StartTime: proto.Int64(time.Date(2026, 5, 22, 10, 45, 0, 0, time.UTC).Unix()),
	}, self)
	if !ok {
		t.Fatal("ParseCallLogRecord returned false")
	}
	if call.Chat != peer {
		t.Fatalf("chat = %s, want %s", call.Chat, peer)
	}
	if call.SenderJID != self.String() {
		t.Fatalf("sender = %q, want %q", call.SenderJID, self.String())
	}
}

func TestCallLogRecordRecognizesPNAndLIDSelfIdentities(t *testing.T) {
	selfPN := types.NewJID("15550000001", types.DefaultUserServer)
	selfLID := types.NewJID("999123456789", types.HiddenUserServer)
	peer := types.NewJID("15550000002", types.DefaultUserServer)

	for _, creator := range []types.JID{selfPN, selfLID} {
		call, ok := ParseCallLogRecord(&waSyncAction.CallLogRecord{
			CallID:         proto.String("call-1"),
			CallCreatorJID: proto.String(creator.String()),
			Participants: []*waSyncAction.CallLogRecord_ParticipantInfo{{
				UserJID: proto.String(peer.String()),
			}},
			StartTime: proto.Int64(time.Date(2026, 5, 22, 10, 45, 0, 0, time.UTC).Unix()),
		}, selfLID, selfPN)
		if !ok {
			t.Fatalf("ParseCallLogRecord returned false for creator %s", creator)
		}
		if call.Chat != peer {
			t.Fatalf("creator %s chat = %s, want %s", creator, call.Chat, peer)
		}
	}
}

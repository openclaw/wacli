package app

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// A history sync carries the roster's labels and messages update them; the
// newest dated one wins and a label change is not stored as a message.
func TestHistorySyncRecordsMemberLabels(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	f.groups[testGroupJID] = testGroup("Team")

	rosterAt := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	changeAt := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	message := func(id string, ts time.Time, msg *waProto.Message) *waHistorySync.HistorySyncMsg {
		return &waHistorySync.HistorySyncMsg{Message: &waWeb.WebMessageInfo{
			Key: &waCommon.MessageKey{
				RemoteJID:   proto.String(testGroupJID.String()),
				FromMe:      proto.Bool(false),
				ID:          proto.String(id),
				Participant: proto.String(testMemberJID.String()),
			},
			MessageTimestamp: proto.Uint64(uint64(ts.Unix())),
			Message:          msg,
		}}
	}
	history := &events.HistorySync{Data: &waHistorySync.HistorySync{
		SyncType: waHistorySync.HistorySync_FULL.Enum(),
		Conversations: []*waHistorySync.Conversation{{
			ID: proto.String(testGroupJID.String()),
			Participant: []*waHistorySync.GroupParticipant{{
				UserJID:     proto.String(testMemberJID.String()),
				MemberLabel: &waE2E.MemberLabel{Label: proto.String("Mom of Alex"), LabelTimestamp: proto.Int64(rosterAt.Unix())},
			}},
			Messages: []*waHistorySync.HistorySyncMsg{
				message("label-change", changeAt, &waProto.Message{ProtocolMessage: &waProto.ProtocolMessage{
					Type:        waE2E.ProtocolMessage_GROUP_MEMBER_LABEL_CHANGE.Enum(),
					MemberLabel: &waE2E.MemberLabel{Label: proto.String("Coach"), LabelTimestamp: proto.Int64(changeAt.Unix())},
				}}),
				message("hello", changeAt.Add(time.Hour), &waProto.Message{ExtendedTextMessage: &waProto.ExtendedTextMessage{
					Text:        proto.String("training at five"),
					ContextInfo: &waProto.ContextInfo{MemberLabel: &waE2E.MemberLabel{Label: proto.String("Coach")}},
				}}),
			},
		}},
	}}

	var messagesStored, lastEvent atomic.Int64
	a.handleHistorySync(context.Background(), SyncOptions{}, history, &messagesStored, &lastEvent, func(string, string) {})

	ps, err := a.db.ListGroupParticipants(testGroupJID.String())
	if err != nil {
		t.Fatalf("ListGroupParticipants: %v", err)
	}
	var label string
	var at *time.Time
	for _, p := range ps {
		if p.UserJID == testMemberJID.String() {
			label, at = p.Label, p.LabelUpdatedAt
		}
	}
	// The ordinary message an hour after the change is the newest evidence.
	if label != "Coach" || at == nil || !at.Equal(changeAt.Add(time.Hour)) {
		t.Fatalf("label = %q at %v, want Coach from the newest message", label, at)
	}
	if _, err := a.db.GetMessage(testGroupJID.String(), "label-change"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("label change stored as a message: err = %v", err)
	}
	if _, err := a.db.GetMessage(testGroupJID.String(), "hello"); err != nil {
		t.Fatalf("ordinary message not stored: %v", err)
	}
}

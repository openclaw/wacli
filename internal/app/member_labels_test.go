package app

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// A history sync carries the roster's labels, a label-change message updates
// one, and neither becomes a stored message.
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

	labels, err := a.db.ListGroupMemberLabels(testGroupJID.String(), testMemberJID.String())
	if err != nil {
		t.Fatalf("ListGroupMemberLabels: %v", err)
	}
	if len(labels) != 2 ||
		labels[0].Label != "Mom of Alex" || !labels[0].SetAt.Equal(rosterAt) || labels[0].Source != store.MemberLabelSourceHistorySync ||
		labels[1].Label != "Coach" || !labels[1].SetAt.Equal(changeAt) || labels[1].Source != store.MemberLabelSourceChange {
		t.Fatalf("labels = %+v, want the roster label followed by the change", labels)
	}
	if _, err := a.db.GetMessage(testGroupJID.String(), "label-change"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("label change stored as a message: err = %v", err)
	}
	if _, err := a.db.GetMessage(testGroupJID.String(), "hello"); err != nil {
		t.Fatalf("ordinary message not stored: %v", err)
	}
}

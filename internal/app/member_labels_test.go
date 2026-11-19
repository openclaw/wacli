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
	"go.mau.fi/whatsmeow/types"
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
	if got := messagesStored.Load(); got != 1 {
		t.Fatalf("messages counted = %d, want only the ordinary message", got)
	}

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

func memberLabelOf(t *testing.T, a *App) string {
	t.Helper()
	ps, err := a.db.ListGroupParticipants(testGroupJID.String())
	if err != nil {
		t.Fatalf("ListGroupParticipants: %v", err)
	}
	for _, p := range ps {
		if p.UserJID == testMemberJID.String() {
			return p.Label
		}
	}
	return ""
}

// The live path as seen on a real account: a member sets a label, then clears
// it, each with a GROUP_MEMBER_LABEL_CHANGE message; plain text carries none.
func TestLiveMemberLabelChangeAndClear(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	f.groups[testGroupJID] = testGroup("Team")

	base := time.Date(2026, 11, 9, 13, 0, 0, 0, time.UTC)
	var stored atomic.Int64
	feed := func(id string, at time.Time, msg *waProto.Message) {
		a.handleLiveSyncMessage(context.Background(), SyncOptions{}, &events.Message{
			Info: types.MessageInfo{
				MessageSource: types.MessageSource{Chat: testGroupJID, Sender: testMemberJID, IsGroup: true},
				ID:            id,
				Timestamp:     at,
			},
			Message: msg,
		}, &stored, func(string, string) {}, nil)
	}
	change := func(label string, at time.Time) *waProto.Message {
		return &waProto.Message{ProtocolMessage: &waProto.ProtocolMessage{
			Type:        waE2E.ProtocolMessage_GROUP_MEMBER_LABEL_CHANGE.Enum(),
			MemberLabel: &waE2E.MemberLabel{Label: proto.String(label), LabelTimestamp: proto.Int64(at.Unix())},
		}}
	}

	feed("first", base, &waProto.Message{Conversation: proto.String("hey")})
	feed("set", base.Add(time.Minute), change("Mom of Alex", base.Add(time.Minute)))
	feed("after-set", base.Add(2*time.Minute), &waProto.Message{Conversation: proto.String("hi")})
	if got := memberLabelOf(t, a); got != "Mom of Alex" {
		t.Fatalf("label after set = %q, want Mom of Alex", got)
	}
	feed("clear", base.Add(time.Hour), change("", base.Add(time.Hour)))
	if got := memberLabelOf(t, a); got != "" {
		t.Fatalf("label after clear = %q, want none", got)
	}
	if got := stored.Load(); got != 2 {
		t.Fatalf("messages counted = %d, want the two text messages only", got)
	}
	for _, id := range []string{"set", "clear"} {
		if _, err := a.db.GetMessage(testGroupJID.String(), id); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("label change %s stored as a message: err = %v", id, err)
		}
	}
}

// History sync names no participant on this account's own messages; their
// label belongs to the linked account, never to the group.
func TestHistoryOwnMemberLabelChangeIsTheLinkedAccounts(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	f.authed = true
	own := f.LinkedJID()
	group := testGroup("Team")
	ownJID, _ := types.ParseJID(own)
	group.Participants = append(group.Participants, types.GroupParticipant{JID: ownJID})
	f.groups[testGroupJID] = group

	at := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	history := &events.HistorySync{Data: &waHistorySync.HistorySync{
		SyncType: waHistorySync.HistorySync_INITIAL_BOOTSTRAP.Enum(),
		Conversations: []*waHistorySync.Conversation{{
			ID: proto.String(testGroupJID.String()),
			Messages: []*waHistorySync.HistorySyncMsg{{Message: &waWeb.WebMessageInfo{
				Key: &waCommon.MessageKey{
					RemoteJID: proto.String(testGroupJID.String()),
					FromMe:    proto.Bool(true),
					ID:        proto.String("own-label"),
				},
				MessageTimestamp: proto.Uint64(uint64(at.Unix())),
				Message: &waProto.Message{ProtocolMessage: &waProto.ProtocolMessage{
					Type:        waE2E.ProtocolMessage_GROUP_MEMBER_LABEL_CHANGE.Enum(),
					MemberLabel: &waE2E.MemberLabel{Label: proto.String("Coach"), LabelTimestamp: proto.Int64(at.Unix())},
				}},
			}}, {Message: &waWeb.WebMessageInfo{
				// An ordinary message, so the group's participant snapshot exists.
				Key: &waCommon.MessageKey{
					RemoteJID:   proto.String(testGroupJID.String()),
					FromMe:      proto.Bool(false),
					ID:          proto.String("hello"),
					Participant: proto.String(testMemberJID.String()),
				},
				MessageTimestamp: proto.Uint64(uint64(at.Add(time.Minute).Unix())),
				Message:          &waProto.Message{Conversation: proto.String("hello")},
			}}},
		}},
	}}
	var messagesStored, lastEvent atomic.Int64
	a.handleHistorySync(context.Background(), SyncOptions{}, history, &messagesStored, &lastEvent, func(string, string) {})

	ps, err := a.db.ListGroupParticipants(testGroupJID.String())
	if err != nil {
		t.Fatalf("ListGroupParticipants: %v", err)
	}
	labels := map[string]string{}
	for _, p := range ps {
		labels[p.UserJID] = p.Label
	}
	if labels[own] != "Coach" {
		t.Fatalf("labels = %v, want Coach on the linked account %s", labels, own)
	}
}

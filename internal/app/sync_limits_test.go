package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestSyncStopsAtMaxMessages(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "123", Server: types.DefaultUserServer}
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	f.connectEvents = []interface{}{historySyncWithTextMessages(chat, base, "m1", "m2", "m3")}

	res, err := a.Sync(context.Background(), SyncOptions{
		Mode:        SyncModeFollow,
		AllowQR:     false,
		MaxMessages: 2,
	})
	if err == nil || !strings.Contains(err.Error(), "sync storage limit reached: message is 2, limit is 2") {
		t.Fatalf("Sync error = %v", err)
	}
	if res.MessagesStored != 2 {
		t.Fatalf("MessagesStored = %d, want 2", res.MessagesStored)
	}
	if n, err := a.db.CountMessages(); err != nil || n != 2 {
		t.Fatalf("db messages = %d, err=%v; want 2", n, err)
	}
}

func TestSyncFlushesHistoryPollsAtMaxMessages(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "123", Server: types.DefaultUserServer}
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	f.connectEvents = []interface{}{historySyncWithPollAndText(chat, base, "poll-limit", "after-limit")}

	res, err := a.Sync(context.Background(), SyncOptions{
		Mode:        SyncModeFollow,
		AllowQR:     false,
		MaxMessages: 1,
	})
	if err == nil || !strings.Contains(err.Error(), "sync storage limit reached: message is 1, limit is 1") {
		t.Fatalf("Sync error = %v", err)
	}
	if res.MessagesStored != 1 {
		t.Fatalf("MessagesStored = %d, want 1", res.MessagesStored)
	}
	poll, err := a.db.GetPoll(chat.String(), "poll-limit")
	if err != nil {
		t.Fatalf("GetPoll after limit cancellation: %v", err)
	}
	if poll.Question != "Limit poll?" {
		t.Fatalf("poll question = %q", poll.Question)
	}
}

func TestSyncFlushesFinalHistoryPollVoteAtMaxMessages(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "123", Server: types.DefaultUserServer}
	voter := types.JID{User: "777", Server: types.DefaultUserServer}
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	pollMsgID := "poll-final-limit"
	if err := a.db.UpsertPoll(store.Poll{
		ChatJID:         chat.String(),
		MsgID:           pollMsgID,
		SenderJID:       chat.String(),
		Question:        "Final limit?",
		Options:         []string{"Yes", "No"},
		SelectableCount: 1,
		CreatedAt:       base,
	}); err != nil {
		t.Fatalf("UpsertPoll: %v", err)
	}
	f.decryptPollVoteFunc = func(_ *events.Message) (*waE2E.PollVoteMessage, error) {
		return &waE2E.PollVoteMessage{SelectedOptions: whatsmeow.HashPollOptions([]string{"Yes"})}, nil
	}
	f.connectEvents = []interface{}{historySyncWithPollVote(chat, voter, base, pollMsgID, "vote-final-limit")}

	res, err := a.Sync(context.Background(), SyncOptions{
		Mode:        SyncModeFollow,
		AllowQR:     false,
		MaxMessages: 1,
	})
	if err == nil || !strings.Contains(err.Error(), "sync storage limit reached: message is 1, limit is 1") {
		t.Fatalf("Sync error = %v", err)
	}
	if res.MessagesStored != 1 {
		t.Fatalf("MessagesStored = %d, want 1", res.MessagesStored)
	}
	votes, err := a.db.ListPollVotes(chat.String(), pollMsgID)
	if err != nil {
		t.Fatalf("ListPollVotes: %v", err)
	}
	if len(votes) != 1 || votes[0].VoterJID != voter.String() || votes[0].VoteMsgID != "vote-final-limit" {
		t.Fatalf("votes = %+v", votes)
	}
}

func TestSyncFlushesFinalLivePollVoteAtMaxMessages(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "123", Server: types.DefaultUserServer}
	voter := types.JID{User: "777", Server: types.DefaultUserServer}
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	pollMsgID := "poll-live-limit"
	if err := a.db.UpsertPoll(store.Poll{
		ChatJID:         chat.String(),
		MsgID:           pollMsgID,
		SenderJID:       chat.String(),
		Question:        "Live limit?",
		Options:         []string{"Yes", "No"},
		SelectableCount: 1,
		CreatedAt:       base,
	}); err != nil {
		t.Fatalf("UpsertPoll: %v", err)
	}
	f.decryptPollVoteFunc = func(_ *events.Message) (*waE2E.PollVoteMessage, error) {
		return &waE2E.PollVoteMessage{SelectedOptions: whatsmeow.HashPollOptions([]string{"Yes"})}, nil
	}
	f.connectEvents = []interface{}{livePollVote(chat, voter, base, pollMsgID, "vote-live-limit")}

	res, err := a.Sync(context.Background(), SyncOptions{
		Mode:        SyncModeFollow,
		AllowQR:     false,
		MaxMessages: 1,
	})
	if err == nil || !strings.Contains(err.Error(), "sync storage limit reached: message is 1, limit is 1") {
		t.Fatalf("Sync error = %v", err)
	}
	if res.MessagesStored != 1 {
		t.Fatalf("MessagesStored = %d, want 1", res.MessagesStored)
	}
	votes, err := a.db.ListPollVotes(chat.String(), pollMsgID)
	if err != nil {
		t.Fatalf("ListPollVotes: %v", err)
	}
	if len(votes) != 1 || votes[0].VoterJID != voter.String() || votes[0].VoteMsgID != "vote-live-limit" {
		t.Fatalf("votes = %+v", votes)
	}
}

func TestSyncRejectsExistingDBOverSizeLimit(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	_, err := a.Sync(context.Background(), SyncOptions{
		Mode:           SyncModeOnce,
		AllowQR:        false,
		MaxDBSizeBytes: 1,
	})
	if err == nil || !strings.Contains(err.Error(), "sync storage limit reached: database size") {
		t.Fatalf("Sync error = %v", err)
	}
	if f.IsConnected() {
		t.Fatal("sync connected before rejecting oversized DB")
	}
}

func TestSyncWarnsWhenStorageUncapped(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	out := captureStderr(t, func() {
		_, err := a.Sync(context.Background(), SyncOptions{
			Mode:         SyncModeOnce,
			AllowQR:      false,
			IdleExit:     time.Millisecond,
			WarnNoLimits: true,
		})
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
	})
	if !strings.Contains(out, "warning: sync storage is uncapped") {
		t.Fatalf("stderr = %q", out)
	}
}

func historySyncWithPollAndText(chat types.JID, start time.Time, pollID, textID string) *events.HistorySync {
	return &events.HistorySync{
		Data: &waHistorySync.HistorySync{
			SyncType: waHistorySync.HistorySync_FULL.Enum(),
			Conversations: []*waHistorySync.Conversation{{
				ID: proto.String(chat.String()),
				Messages: []*waHistorySync.HistorySyncMsg{
					{
						Message: &waWeb.WebMessageInfo{
							Key: &waCommon.MessageKey{
								RemoteJID: proto.String(chat.String()),
								FromMe:    proto.Bool(false),
								ID:        proto.String(pollID),
							},
							MessageTimestamp: proto.Uint64(uint64(start.Unix())),
							Message: &waProto.Message{
								PollCreationMessageV3: &waProto.PollCreationMessage{
									Name: proto.String("Limit poll?"),
									Options: []*waProto.PollCreationMessage_Option{
										{OptionName: proto.String("Yes")},
										{OptionName: proto.String("No")},
									},
								},
							},
						},
					},
					{
						Message: &waWeb.WebMessageInfo{
							Key: &waCommon.MessageKey{
								RemoteJID: proto.String(chat.String()),
								FromMe:    proto.Bool(false),
								ID:        proto.String(textID),
							},
							MessageTimestamp: proto.Uint64(uint64(start.Add(time.Second).Unix())),
							Message:          &waProto.Message{Conversation: proto.String("text " + textID)},
						},
					},
				},
			}},
		},
	}
}

func livePollVote(chat, voter types.JID, ts time.Time, pollID, voteID string) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:   chat,
				Sender: voter,
			},
			ID:        voteID,
			Timestamp: ts,
		},
		Message: &waProto.Message{
			PollUpdateMessage: &waProto.PollUpdateMessage{
				PollCreationMessageKey: &waCommon.MessageKey{
					ID:        proto.String(pollID),
					RemoteJID: proto.String(chat.String()),
				},
			},
		},
	}
}

func historySyncWithPollVote(chat, voter types.JID, ts time.Time, pollID, voteID string) *events.HistorySync {
	return &events.HistorySync{
		Data: &waHistorySync.HistorySync{
			SyncType: waHistorySync.HistorySync_FULL.Enum(),
			Conversations: []*waHistorySync.Conversation{{
				ID: proto.String(chat.String()),
				Messages: []*waHistorySync.HistorySyncMsg{{
					Message: &waWeb.WebMessageInfo{
						Key: &waCommon.MessageKey{
							RemoteJID: proto.String(chat.String()),
							FromMe:    proto.Bool(false),
							ID:        proto.String(voteID),
						},
						Participant:      proto.String(voter.String()),
						MessageTimestamp: proto.Uint64(uint64(ts.Unix())),
						Message: &waProto.Message{
							PollUpdateMessage: &waProto.PollUpdateMessage{
								PollCreationMessageKey: &waCommon.MessageKey{
									ID:        proto.String(pollID),
									RemoteJID: proto.String(chat.String()),
								},
							},
						},
					},
				}},
			}},
		},
	}
}

func historySyncWithTextMessages(chat types.JID, start time.Time, ids ...string) *events.HistorySync {
	msgs := make([]*waHistorySync.HistorySyncMsg, 0, len(ids))
	for i, id := range ids {
		msgs = append(msgs, &waHistorySync.HistorySyncMsg{
			Message: &waWeb.WebMessageInfo{
				Key: &waCommon.MessageKey{
					RemoteJID: proto.String(chat.String()),
					FromMe:    proto.Bool(false),
					ID:        proto.String(id),
				},
				MessageTimestamp: proto.Uint64(uint64(start.Add(time.Duration(i) * time.Second).Unix())),
				Message:          &waProto.Message{Conversation: proto.String("text " + id)},
			},
		})
	}
	return &events.HistorySync{
		Data: &waHistorySync.HistorySync{
			SyncType: waHistorySync.HistorySync_FULL.Enum(),
			Conversations: []*waHistorySync.Conversation{{
				ID:       proto.String(chat.String()),
				Messages: msgs,
			}},
		},
	}
}

package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

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

func TestLiveSyncStoresPollCreation(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "555", Server: types.DefaultUserServer}
	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat},
			ID:            "POLL-1",
			Timestamp:     time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC),
		},
		Message: &waProto.Message{
			PollCreationMessageV3: &waProto.PollCreationMessage{
				Name: proto.String("Pizza?"),
				Options: []*waProto.PollCreationMessage_Option{
					{OptionName: proto.String("Yes")},
					{OptionName: proto.String("No")},
				},
				SelectableOptionsCount: proto.Uint32(1),
			},
		},
	}

	var messagesStored atomic.Int64
	a.handleLiveSyncMessage(context.Background(), SyncOptions{}, evt, &messagesStored, func(string, string) {}, nil)

	if messagesStored.Load() != 1 {
		t.Fatalf("messagesStored = %d", messagesStored.Load())
	}
	poll, err := a.db.GetPoll(chat.String(), "POLL-1")
	if err != nil {
		t.Fatalf("GetPoll: %v", err)
	}
	if poll.Question != "Pizza?" {
		t.Fatalf("question = %q", poll.Question)
	}
	if len(poll.Options) != 2 || poll.Options[0] != "Yes" || poll.Options[1] != "No" {
		t.Fatalf("options = %v", poll.Options)
	}
	if poll.SelectableCount != 1 {
		t.Fatalf("selectable = %d", poll.SelectableCount)
	}
}

func TestLiveSyncStoresPollCreationUnderCanonicalPN(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	lid := types.JID{User: "999", Server: types.HiddenUserServer}
	pn := types.JID{User: "15551234567", Server: types.DefaultUserServer}
	senderLID := types.JID{User: "777", Server: types.HiddenUserServer}
	senderPN := types.JID{User: "15557654321", Server: types.DefaultUserServer}
	f.lids[lid] = pn
	f.lids[senderLID] = senderPN

	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: lid, Sender: senderLID},
			ID:            "POLL-LID",
			Timestamp:     time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC),
		},
		Message: &waProto.Message{
			PollCreationMessageV3: &waProto.PollCreationMessage{
				Name: proto.String("Canonical?"),
				Options: []*waProto.PollCreationMessage_Option{
					{OptionName: proto.String("Yes")},
					{OptionName: proto.String("No")},
				},
			},
		},
	}

	var messagesStored atomic.Int64
	a.handleLiveSyncMessage(context.Background(), SyncOptions{}, evt, &messagesStored, func(string, string) {}, nil)

	poll, err := a.db.GetPoll(pn.String(), "POLL-LID")
	if err != nil {
		t.Fatalf("GetPoll canonical PN: %v", err)
	}
	if poll.SenderJID != senderPN.String() {
		t.Fatalf("SenderJID = %q, want %q", poll.SenderJID, senderPN.String())
	}
	if _, err := a.db.GetPoll(lid.String(), "POLL-LID"); err == nil {
		t.Fatalf("poll was also stored under raw LID")
	}
}

func TestLiveSyncPreservesRawGroupPollSender(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "12345", Server: types.GroupServer}
	senderLID := types.JID{User: "777", Server: types.HiddenUserServer}
	senderPN := types.JID{User: "15557654321", Server: types.DefaultUserServer}
	f.lids[senderLID] = senderPN

	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: senderLID, IsGroup: true},
			ID:            "POLL-GROUP-LID",
			Timestamp:     time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC),
		},
		Message: &waProto.Message{
			PollCreationMessageV3: &waProto.PollCreationMessage{
				Name:    proto.String("Group?"),
				Options: []*waProto.PollCreationMessage_Option{{OptionName: proto.String("Yes")}},
			},
		},
	}

	var messagesStored atomic.Int64
	a.handleLiveSyncMessage(context.Background(), SyncOptions{}, evt, &messagesStored, func(string, string) {}, nil)

	poll, err := a.db.GetPoll(chat.String(), "POLL-GROUP-LID")
	if err != nil {
		t.Fatalf("GetPoll: %v", err)
	}
	if poll.SenderJID != senderLID.String() {
		t.Fatalf("SenderJID = %q, want raw %q", poll.SenderJID, senderLID.String())
	}
}

func TestHistorySyncStoresWrappedSelfPollWithLinkedSender(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "555", Server: types.DefaultUserServer}
	created := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	hist := &waWeb.WebMessageInfo{
		Key: &waCommon.MessageKey{
			RemoteJID: proto.String(chat.String()),
			FromMe:    proto.Bool(true),
			ID:        proto.String("POLL-HIST"),
		},
		MessageTimestamp: proto.Uint64(uint64(created.Unix())),
		Message: &waProto.Message{
			EphemeralMessage: &waProto.FutureProofMessage{
				Message: &waProto.Message{
					PollCreationMessageV3: &waProto.PollCreationMessage{
						Name: proto.String("Wrapped?"),
						Options: []*waProto.PollCreationMessage_Option{
							{OptionName: proto.String("Yes")},
							{OptionName: proto.String("No")},
						},
						SelectableOptionsCount: proto.Uint32(1),
					},
				},
			},
		},
	}
	history := &events.HistorySync{
		Data: &waHistorySync.HistorySync{
			SyncType: waHistorySync.HistorySync_FULL.Enum(),
			Conversations: []*waHistorySync.Conversation{{
				ID:       proto.String(chat.String()),
				Messages: []*waHistorySync.HistorySyncMsg{{Message: hist}},
			}},
		},
	}

	var messagesStored atomic.Int64
	var lastEvent atomic.Int64
	a.handleHistorySync(context.Background(), SyncOptions{}, history, &messagesStored, &lastEvent, func(string, string) {})

	poll, err := a.db.GetPoll(chat.String(), "POLL-HIST")
	if err != nil {
		t.Fatalf("GetPoll: %v", err)
	}
	if poll.Question != "Wrapped?" {
		t.Fatalf("Question = %q", poll.Question)
	}
	if poll.SenderJID != f.LinkedJID() {
		t.Fatalf("SenderJID = %q, want %q", poll.SenderJID, f.LinkedJID())
	}
	msg, err := a.db.GetMessage(chat.String(), "POLL-HIST")
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if msg.Text != "Poll: Wrapped?" || msg.DisplayText != "Poll: Wrapped?" {
		t.Fatalf("message text/display = %q/%q", msg.Text, msg.DisplayText)
	}
}

func TestHistorySyncStoresSelfGroupPollWithLIDSender(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	group := types.JID{User: "120363001234567890", Server: types.GroupServer}
	ownPN := types.JID{User: "1234567890", Server: types.DefaultUserServer}
	ownLID := types.JID{User: "999123456789", Server: types.HiddenUserServer}
	f.groups[group] = &types.GroupInfo{JID: group, AddressingMode: types.AddressingModeLID}
	f.lids[ownLID] = ownPN

	created := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	hist := &waWeb.WebMessageInfo{
		Key: &waCommon.MessageKey{
			RemoteJID: proto.String(group.String()),
			FromMe:    proto.Bool(true),
			ID:        proto.String("POLL-GROUP-HIST"),
		},
		MessageTimestamp: proto.Uint64(uint64(created.Unix())),
		Message: &waProto.Message{
			PollCreationMessageV3: &waProto.PollCreationMessage{
				Name:    proto.String("Group history?"),
				Options: []*waProto.PollCreationMessage_Option{{OptionName: proto.String("Yes")}, {OptionName: proto.String("No")}},
			},
		},
	}
	history := &events.HistorySync{
		Data: &waHistorySync.HistorySync{
			SyncType: waHistorySync.HistorySync_FULL.Enum(),
			Conversations: []*waHistorySync.Conversation{{
				ID:       proto.String(group.String()),
				Messages: []*waHistorySync.HistorySyncMsg{{Message: hist}},
			}},
		},
	}

	var messagesStored atomic.Int64
	var lastEvent atomic.Int64
	a.handleHistorySync(context.Background(), SyncOptions{}, history, &messagesStored, &lastEvent, func(string, string) {})

	poll, err := a.db.GetPoll(group.String(), "POLL-GROUP-HIST")
	if err != nil {
		t.Fatalf("GetPoll: %v", err)
	}
	if poll.SenderJID != ownLID.String() {
		t.Fatalf("SenderJID = %q, want LID %q", poll.SenderJID, ownLID.String())
	}
}

func TestHistorySyncStoresPollVoteBeforeCreation(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "555", Server: types.DefaultUserServer}
	voter := types.JID{User: "777", Server: types.DefaultUserServer}
	pollMsgID := "POLL-HIST-ORDER"
	created := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	f.decryptPollVoteFunc = func(_ *events.Message) (*waE2E.PollVoteMessage, error) {
		return &waE2E.PollVoteMessage{SelectedOptions: whatsmeow.HashPollOptions([]string{"Yes"})}, nil
	}

	vote := &waWeb.WebMessageInfo{
		Key: &waCommon.MessageKey{
			RemoteJID: proto.String(chat.String()),
			FromMe:    proto.Bool(false),
			ID:        proto.String("VOTE-HIST-ORDER"),
		},
		Participant:      proto.String(voter.String()),
		MessageTimestamp: proto.Uint64(uint64(created.Add(time.Minute).Unix())),
		Message: &waProto.Message{
			PollUpdateMessage: &waProto.PollUpdateMessage{
				PollCreationMessageKey: &waCommon.MessageKey{
					ID:        proto.String(pollMsgID),
					RemoteJID: proto.String(chat.String()),
				},
			},
		},
	}
	creation := &waWeb.WebMessageInfo{
		Key: &waCommon.MessageKey{
			RemoteJID: proto.String(chat.String()),
			FromMe:    proto.Bool(false),
			ID:        proto.String(pollMsgID),
		},
		MessageTimestamp: proto.Uint64(uint64(created.Unix())),
		Message: &waProto.Message{
			PollCreationMessageV3: &waProto.PollCreationMessage{
				Name: proto.String("Dinner?"),
				Options: []*waProto.PollCreationMessage_Option{
					{OptionName: proto.String("Yes")},
					{OptionName: proto.String("No")},
				},
				SelectableOptionsCount: proto.Uint32(1),
			},
		},
	}
	history := &events.HistorySync{
		Data: &waHistorySync.HistorySync{
			SyncType: waHistorySync.HistorySync_FULL.Enum(),
			Conversations: []*waHistorySync.Conversation{{
				ID:       proto.String(chat.String()),
				Messages: []*waHistorySync.HistorySyncMsg{{Message: vote}, {Message: creation}},
			}},
		},
	}

	var messagesStored atomic.Int64
	var lastEvent atomic.Int64
	a.handleHistorySync(context.Background(), SyncOptions{}, history, &messagesStored, &lastEvent, func(string, string) {})

	votes, err := a.db.ListPollVotes(chat.String(), pollMsgID)
	if err != nil {
		t.Fatalf("ListPollVotes: %v", err)
	}
	if len(votes) != 1 {
		t.Fatalf("vote count = %d", len(votes))
	}
	if votes[0].VoterJID != voter.String() || !sameStringSet(votes[0].Selected, []string{"Yes"}) {
		t.Fatalf("vote = %+v", votes[0])
	}
}

func TestLiveSyncDecryptsPollAddOptionBeforeVote(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "555", Server: types.DefaultUserServer}
	pollMsgID := "POLL-ADD"
	created := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)

	creationEvt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat},
			ID:            pollMsgID,
			Timestamp:     created,
		},
		Message: &waProto.Message{
			PollCreationMessageV3: &waProto.PollCreationMessage{
				Name:                   proto.String("Dinner?"),
				Options:                []*waProto.PollCreationMessage_Option{{OptionName: proto.String("Yes")}, {OptionName: proto.String("No")}},
				SelectableOptionsCount: proto.Uint32(1),
			},
		},
	}
	var messagesStored atomic.Int64
	a.handleLiveSyncMessage(context.Background(), SyncOptions{}, creationEvt, &messagesStored, func(string, string) {}, nil)

	f.decryptSecretFunc = func(_ *events.Message) (*waE2E.Message, error) {
		return &waE2E.Message{
			PollAddOptionMessage: &waE2E.PollAddOptionMessage{
				AddOption: &waE2E.PollCreationMessage_Option{OptionName: proto.String("Maybe")},
			},
		}, nil
	}
	addEvt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat},
			ID:            "ADD-1",
			Timestamp:     created.Add(time.Minute),
		},
		Message: &waProto.Message{
			SecretEncryptedMessage: &waE2E.SecretEncryptedMessage{
				TargetMessageKey: &waCommon.MessageKey{
					ID:        proto.String(pollMsgID),
					RemoteJID: proto.String(chat.String()),
				},
				SecretEncType: waE2E.SecretEncryptedMessage_POLL_ADD_OPTION.Enum(),
			},
		},
	}
	a.handleLiveSyncMessage(context.Background(), SyncOptions{}, addEvt, &messagesStored, func(string, string) {}, nil)

	poll, err := a.db.GetPoll(chat.String(), pollMsgID)
	if err != nil {
		t.Fatalf("GetPoll: %v", err)
	}
	if !sameStringSet(poll.Options, []string{"Yes", "No", "Maybe"}) {
		t.Fatalf("options = %v", poll.Options)
	}

	f.decryptPollVoteFunc = func(_ *events.Message) (*waE2E.PollVoteMessage, error) {
		return &waE2E.PollVoteMessage{SelectedOptions: whatsmeow.HashPollOptions([]string{"Maybe"})}, nil
	}
	voter := types.JID{User: "777", Server: types.DefaultUserServer}
	voteEvt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: voter},
			ID:            "VOTE-ADD",
			Timestamp:     created.Add(2 * time.Minute),
		},
		Message: &waProto.Message{
			PollUpdateMessage: &waProto.PollUpdateMessage{
				PollCreationMessageKey: &waCommon.MessageKey{
					ID:        proto.String(pollMsgID),
					RemoteJID: proto.String(chat.String()),
				},
			},
		},
	}
	a.handleLiveSyncMessage(context.Background(), SyncOptions{}, voteEvt, &messagesStored, func(string, string) {}, nil)

	votes, err := a.db.ListPollVotes(chat.String(), pollMsgID)
	if err != nil {
		t.Fatalf("ListPollVotes: %v", err)
	}
	if len(votes) != 1 || !sameStringSet(votes[0].Selected, []string{"Maybe"}) {
		t.Fatalf("votes = %+v", votes)
	}
}

func TestLiveSyncDecryptsAndStoresPollVote(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "555", Server: types.DefaultUserServer}
	pollMsgID := "POLL-1"
	options := []string{"Yes", "No", "Maybe"}

	// Persist the poll first.
	creationEvt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat, IsFromMe: true},
			ID:            pollMsgID,
			Timestamp:     time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC),
		},
		Message: &waProto.Message{
			PollCreationMessageV3: &waProto.PollCreationMessage{
				Name:                   proto.String("Pizza?"),
				Options:                []*waProto.PollCreationMessage_Option{{OptionName: proto.String("Yes")}, {OptionName: proto.String("No")}, {OptionName: proto.String("Maybe")}},
				SelectableOptionsCount: proto.Uint32(2),
			},
		},
	}
	var messagesStored atomic.Int64
	a.handleLiveSyncMessage(context.Background(), SyncOptions{}, creationEvt, &messagesStored, func(string, string) {}, nil)

	// Voter votes for Yes + Maybe.
	voter := types.JID{User: "777", Server: types.DefaultUserServer}
	hashes := whatsmeow.HashPollOptions([]string{"Yes", "Maybe"})
	f.decryptPollVoteFunc = func(_ *events.Message) (*waE2E.PollVoteMessage, error) {
		return &waE2E.PollVoteMessage{SelectedOptions: hashes}, nil
	}

	voteEvt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: voter},
			ID:            "VOTE-1",
			Timestamp:     time.Date(2026, 5, 9, 12, 5, 0, 0, time.UTC),
		},
		Message: &waProto.Message{
			PollUpdateMessage: &waProto.PollUpdateMessage{
				PollCreationMessageKey: &waProto.MessageKey{
					ID:          proto.String(pollMsgID),
					RemoteJID:   proto.String(chat.String()),
					Participant: proto.String(chat.String()),
					FromMe:      proto.Bool(true),
				},
			},
		},
	}
	a.handleLiveSyncMessage(context.Background(), SyncOptions{}, voteEvt, &messagesStored, func(string, string) {}, nil)

	votes, err := a.db.ListPollVotes(chat.String(), pollMsgID)
	if err != nil {
		t.Fatalf("ListPollVotes: %v", err)
	}
	if len(votes) != 1 {
		t.Fatalf("vote count = %d", len(votes))
	}
	if votes[0].VoterJID != voter.String() {
		t.Fatalf("voter = %q", votes[0].VoterJID)
	}
	want := []string{"Yes", "Maybe"}
	if !sameStringSet(votes[0].Selected, want) {
		t.Fatalf("selected = %v want %v", votes[0].Selected, want)
	}
	_ = options
}

func TestLiveSyncKeepsNewerSameSecondPollVote(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "555", Server: types.DefaultUserServer}
	voter := types.JID{User: "777", Server: types.DefaultUserServer}
	pollMsgID := "POLL-1"
	base := time.Date(2026, 5, 9, 12, 5, 0, 0, time.UTC)
	var messagesStored atomic.Int64
	a.handleLiveSyncMessage(context.Background(), SyncOptions{}, &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat, IsFromMe: true},
			ID:            pollMsgID,
			Timestamp:     base.Add(-time.Minute),
		},
		Message: &waProto.Message{
			PollCreationMessageV3: &waProto.PollCreationMessage{
				Name: proto.String("Pizza?"),
				Options: []*waProto.PollCreationMessage_Option{
					{OptionName: proto.String("Old")},
					{OptionName: proto.String("New")},
				},
				SelectableOptionsCount: proto.Uint32(1),
			},
		},
	}, &messagesStored, func(string, string) {}, nil)

	f.decryptPollVoteFunc = func(evt *events.Message) (*waE2E.PollVoteMessage, error) {
		selected := []string{"Old"}
		if evt.Info.ID == "VOTE-NEW" {
			selected = []string{"New"}
		}
		return &waE2E.PollVoteMessage{SelectedOptions: whatsmeow.HashPollOptions(selected)}, nil
	}
	makeVote := func(id string, senderTs time.Time) *events.Message {
		return &events.Message{
			Info: types.MessageInfo{
				MessageSource: types.MessageSource{Chat: chat, Sender: voter},
				ID:            id,
				Timestamp:     base,
			},
			Message: &waProto.Message{
				PollUpdateMessage: &waProto.PollUpdateMessage{
					PollCreationMessageKey: &waProto.MessageKey{
						ID:        proto.String(pollMsgID),
						RemoteJID: proto.String(chat.String()),
					},
					SenderTimestampMS: proto.Int64(senderTs.UnixMilli()),
				},
			},
		}
	}
	a.handleLiveSyncMessage(context.Background(), SyncOptions{}, makeVote("VOTE-NEW", base.Add(900*time.Millisecond)), &messagesStored, func(string, string) {}, nil)
	a.handleLiveSyncMessage(context.Background(), SyncOptions{}, makeVote("VOTE-OLD", base.Add(100*time.Millisecond)), &messagesStored, func(string, string) {}, nil)

	votes, err := a.db.ListPollVotes(chat.String(), pollMsgID)
	if err != nil {
		t.Fatalf("ListPollVotes: %v", err)
	}
	if len(votes) != 1 || votes[0].VoteMsgID != "VOTE-NEW" || !sameStringSet(votes[0].Selected, []string{"New"}) {
		t.Fatalf("votes = %+v", votes)
	}
}

func TestLiveSyncDeletesRetractedPollVote(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "555", Server: types.DefaultUserServer}
	voter := types.JID{User: "777", Server: types.DefaultUserServer}
	pollMsgID := "POLL-RETRACT"
	base := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	var messagesStored atomic.Int64
	a.handleLiveSyncMessage(context.Background(), SyncOptions{}, &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat},
			ID:            pollMsgID,
			Timestamp:     base,
		},
		Message: &waProto.Message{
			PollCreationMessageV3: &waProto.PollCreationMessage{
				Name:    proto.String("Lunch?"),
				Options: []*waProto.PollCreationMessage_Option{{OptionName: proto.String("Yes")}, {OptionName: proto.String("No")}},
			},
		},
	}, &messagesStored, func(string, string) {}, nil)

	f.decryptPollVoteFunc = func(evt *events.Message) (*waE2E.PollVoteMessage, error) {
		if evt.Info.ID == "VOTE-CLEAR" {
			return &waE2E.PollVoteMessage{}, nil
		}
		return &waE2E.PollVoteMessage{SelectedOptions: whatsmeow.HashPollOptions([]string{"Yes"})}, nil
	}
	makeVote := func(id string, ts time.Time) *events.Message {
		return &events.Message{
			Info: types.MessageInfo{
				MessageSource: types.MessageSource{Chat: chat, Sender: voter},
				ID:            id,
				Timestamp:     ts,
			},
			Message: &waProto.Message{
				PollUpdateMessage: &waProto.PollUpdateMessage{
					PollCreationMessageKey: &waCommon.MessageKey{
						ID:        proto.String(pollMsgID),
						RemoteJID: proto.String(chat.String()),
					},
					SenderTimestampMS: proto.Int64(ts.UnixMilli()),
				},
			},
		}
	}
	a.handleLiveSyncMessage(context.Background(), SyncOptions{}, makeVote("VOTE-YES", base.Add(time.Minute)), &messagesStored, func(string, string) {}, nil)
	a.handleLiveSyncMessage(context.Background(), SyncOptions{}, makeVote("VOTE-CLEAR", base.Add(2*time.Minute)), &messagesStored, func(string, string) {}, nil)

	votes, err := a.db.ListPollVotes(chat.String(), pollMsgID)
	if err != nil {
		t.Fatalf("ListPollVotes: %v", err)
	}
	if len(votes) != 0 {
		t.Fatalf("votes after retraction = %+v", votes)
	}
}

func TestLiveSyncWarnsWhenPollVoteRefersToUnknownPoll(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	f.decryptPollVoteFunc = func(_ *events.Message) (*waE2E.PollVoteMessage, error) {
		return &waE2E.PollVoteMessage{}, nil
	}

	chat := types.JID{User: "555", Server: types.DefaultUserServer}
	voteEvt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat},
			ID:            "VOTE-X",
			Timestamp:     time.Now(),
		},
		Message: &waProto.Message{
			PollUpdateMessage: &waProto.PollUpdateMessage{
				PollCreationMessageKey: &waProto.MessageKey{
					ID:        proto.String("UNKNOWN-POLL"),
					RemoteJID: proto.String(chat.String()),
				},
			},
		},
	}

	var messagesStored atomic.Int64
	out := captureStderr(t, func() {
		a.handleLiveSyncMessage(context.Background(), SyncOptions{}, voteEvt, &messagesStored, func(string, string) {}, nil)
	})
	if !contains(out, "poll_vote_unknown_poll") && !contains(out, "warning: poll vote") {
		t.Fatalf("expected unknown-poll warning, got:\n%s", out)
	}
	votes, err := a.db.ListPollVotes(chat.String(), "UNKNOWN-POLL")
	if err != nil {
		t.Fatal(err)
	}
	if len(votes) != 0 {
		t.Fatalf("expected no votes stored, got %d", len(votes))
	}
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	count := func(s []string, v string) int {
		n := 0
		for _, x := range s {
			if x == v {
				n++
			}
		}
		return n
	}
	for _, v := range a {
		if count(a, v) != count(b, v) {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	if len(s) < len(sub) {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

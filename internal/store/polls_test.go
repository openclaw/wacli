package store

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestUpsertAndGetPoll(t *testing.T) {
	db := openTestDB(t)

	created := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	in := Poll{
		ChatJID:         "15551112222@s.whatsapp.net",
		MsgID:           "POLL-1",
		SenderJID:       "15553334444@s.whatsapp.net",
		Question:        "Pizza?",
		Options:         []string{"Yes", "No", "Maybe"},
		SelectableCount: 2,
		CreatedAt:       created,
	}
	if err := db.UpsertPoll(in); err != nil {
		t.Fatalf("UpsertPoll: %v", err)
	}

	got, err := db.GetPoll(in.ChatJID, in.MsgID)
	if err != nil {
		t.Fatalf("GetPoll: %v", err)
	}
	if got.Question != in.Question {
		t.Fatalf("question = %q", got.Question)
	}
	if !reflect.DeepEqual(got.Options, in.Options) {
		t.Fatalf("options = %v", got.Options)
	}
	if got.SelectableCount != 2 {
		t.Fatalf("selectable = %d", got.SelectableCount)
	}
	if !got.CreatedAt.Equal(created) {
		t.Fatalf("created_at = %v want %v", got.CreatedAt, created)
	}
}

func TestUpsertPollIsIdempotent(t *testing.T) {
	db := openTestDB(t)

	p := Poll{
		ChatJID:         "chat@s.whatsapp.net",
		MsgID:           "P1",
		Question:        "v1",
		Options:         []string{"a", "b"},
		SelectableCount: 1,
		CreatedAt:       time.Now().UTC(),
	}
	if err := db.UpsertPoll(p); err != nil {
		t.Fatal(err)
	}
	p.Question = "v2"
	if err := db.UpsertPoll(p); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetPoll(p.ChatJID, p.MsgID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Question != "v2" {
		t.Fatalf("question = %q want v2", got.Question)
	}

	n := countRows(t, db.sql, "SELECT COUNT(*) FROM polls")
	if n != 1 {
		t.Fatalf("polls row count = %d", n)
	}
}

func TestUpsertPollVoteReplacesPriorVote(t *testing.T) {
	db := openTestDB(t)

	if err := db.UpsertPoll(Poll{
		ChatJID:         "chat@s.whatsapp.net",
		MsgID:           "P1",
		Question:        "Q?",
		Options:         []string{"a", "b"},
		SelectableCount: 1,
		CreatedAt:       time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	first := PollVote{
		ChatJID:   "chat@s.whatsapp.net",
		PollMsgID: "P1",
		VoterJID:  "voter@s.whatsapp.net",
		VoteMsgID: "V1",
		Selected:  []string{"a"},
		VotedAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	if err := db.UpsertPollVote(first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.VoteMsgID = "V2"
	second.Selected = []string{"b"}
	second.VotedAt = time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	if err := db.UpsertPollVote(second); err != nil {
		t.Fatal(err)
	}

	votes, err := db.ListPollVotes("chat@s.whatsapp.net", "P1")
	if err != nil {
		t.Fatal(err)
	}
	if len(votes) != 1 {
		t.Fatalf("vote count = %d", len(votes))
	}
	if votes[0].VoteMsgID != "V2" || !reflect.DeepEqual(votes[0].Selected, []string{"b"}) {
		t.Fatalf("vote = %+v", votes[0])
	}
}

func TestListPollsFilterAndOrder(t *testing.T) {
	db := openTestDB(t)

	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	polls := []Poll{
		{ChatJID: "a@s.whatsapp.net", MsgID: "P1", Question: "old a", Options: []string{"x", "y"}, CreatedAt: now.Add(-2 * time.Hour)},
		{ChatJID: "a@s.whatsapp.net", MsgID: "P2", Question: "new a", Options: []string{"x", "y"}, CreatedAt: now},
		{ChatJID: "b@s.whatsapp.net", MsgID: "P3", Question: "b only", Options: []string{"x", "y"}, CreatedAt: now.Add(-1 * time.Hour)},
	}
	for _, p := range polls {
		if err := db.UpsertPoll(p); err != nil {
			t.Fatalf("UpsertPoll: %v", err)
		}
	}

	all, err := db.ListPolls(PollListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("len(all) = %d", len(all))
	}
	if all[0].MsgID != "P2" {
		t.Fatalf("expected P2 first, got %s", all[0].MsgID)
	}

	onlyA, err := db.ListPolls(PollListFilter{ChatJID: "a@s.whatsapp.net"})
	if err != nil {
		t.Fatal(err)
	}
	if len(onlyA) != 2 {
		t.Fatalf("len(onlyA) = %d", len(onlyA))
	}
}

func TestDeletePollRemovesVotes(t *testing.T) {
	db := openTestDB(t)

	if err := db.UpsertPoll(Poll{
		ChatJID: "chat@s.whatsapp.net", MsgID: "P1",
		Question: "Q?", Options: []string{"a", "b"}, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertPollVote(PollVote{
		ChatJID: "chat@s.whatsapp.net", PollMsgID: "P1", VoterJID: "v@s.whatsapp.net",
		VoteMsgID: "V1", Selected: []string{"a"}, VotedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.DeletePoll("chat@s.whatsapp.net", "P1"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetPoll("chat@s.whatsapp.net", "P1"); err == nil {
		t.Fatal("expected GetPoll to return ErrNoRows after delete")
	} else if !IsPollNotFound(err) {
		t.Fatalf("expected sql.ErrNoRows, got %v", err)
	}
	votes, err := db.ListPollVotes("chat@s.whatsapp.net", "P1")
	if err != nil {
		t.Fatal(err)
	}
	if len(votes) != 0 {
		t.Fatalf("expected votes wiped, got %d", len(votes))
	}
}

func TestUpsertPollRejectsMissingKey(t *testing.T) {
	db := openTestDB(t)
	err := db.UpsertPoll(Poll{Question: "Q?", Options: []string{"a", "b"}})
	if err == nil {
		t.Fatal("expected error for missing chat/msg id")
	}
	if errors.Is(err, nil) {
		t.Fatal("nil error wrapping unexpected")
	}
}

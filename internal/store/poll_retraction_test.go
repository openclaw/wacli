package store

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestPollRetractionSurvivesReplayAndRestart(t *testing.T) {
	for _, initialVote := range []bool{false, true} {
		t.Run(map[bool]string{false: "retraction first", true: "vote then retraction"}[initialVote], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "wacli.db")
			db, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			base := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
			vote := PollVote{
				ChatJID: "group@g.us", PollMsgID: "poll", VoterJID: "voter@s.whatsapp.net",
				VoteMsgID: "vote", Selected: []string{"yes"}, VotedAt: base.Add(100 * time.Millisecond),
			}
			if initialVote {
				if err := db.UpsertPollVote(vote); err != nil {
					t.Fatal(err)
				}
			}
			retraction := vote
			retraction.VoteMsgID = "retraction"
			retraction.Selected = nil
			retraction.VotedAt = base.Add(900 * time.Millisecond)
			if err := db.UpsertPollVote(retraction); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}

			if err := db.UpsertPollVote(vote); err != nil {
				t.Fatal(err)
			}
			votes, err := db.ListPollVotes(vote.ChatJID, vote.PollMsgID)
			if err != nil {
				t.Fatal(err)
			}
			if len(votes) != 0 {
				t.Fatalf("old replay restored retracted votes: %+v", votes)
			}
		})
	}
}

func TestPollVoteAfterRetractionIsVisible(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "known option", true: "unknown option hash"}[unknown], func(t *testing.T) {
			db := openTestDB(t)
			vote := PollVote{
				ChatJID: "group@g.us", PollMsgID: "poll", VoterJID: "voter@s.whatsapp.net",
				VoteMsgID: "retraction", VotedAt: time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC),
			}
			if err := db.UpsertPollVote(vote); err != nil {
				t.Fatal(err)
			}
			vote.VoteMsgID = "new-vote"
			vote.VotedAt = vote.VotedAt.Add(time.Millisecond)
			if unknown {
				vote.UnknownHashes = []string{"001122"}
			} else {
				vote.Selected = []string{"yes"}
			}

			if err := db.UpsertPollVote(vote); err != nil {
				t.Fatal(err)
			}
			votes, err := db.ListPollVotes(vote.ChatJID, vote.PollMsgID)
			if err != nil {
				t.Fatal(err)
			}
			if len(votes) != 1 || votes[0].VoteMsgID != vote.VoteMsgID || !reflect.DeepEqual(votes[0].UnknownHashes, vote.UnknownHashes) {
				t.Fatalf("new vote = %+v, want %+v", votes, vote)
			}
			if !unknown && !reflect.DeepEqual(votes[0].Selected, vote.Selected) {
				t.Fatalf("selection = %v, want %v", votes[0].Selected, vote.Selected)
			}
		})
	}
}

func TestPollRetractionSurvivesVoterIdentityMerge(t *testing.T) {
	db := openTestDB(t)
	lid, pn := "777@lid", "777@s.whatsapp.net"
	vote := PollVote{
		ChatJID: "group@g.us", PollMsgID: "poll", VoterJID: pn,
		VoteMsgID: "vote", Selected: []string{"yes"}, VotedAt: time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC),
	}
	if err := db.UpsertPollVote(vote); err != nil {
		t.Fatal(err)
	}
	retraction := vote
	retraction.VoterJID = lid
	retraction.VoteMsgID = "retraction"
	retraction.Selected = nil
	retraction.VotedAt = vote.VotedAt.Add(time.Millisecond)
	if err := db.UpsertPollVote(retraction); err != nil {
		t.Fatal(err)
	}

	if err := db.MigrateLIDToPN(lid, pn); err != nil {
		t.Fatal(err)
	}
	votes, err := db.ListPollVotes(vote.ChatJID, vote.PollMsgID)
	if err != nil {
		t.Fatal(err)
	}
	if len(votes) != 0 {
		t.Fatalf("identity merge restored a retracted vote: %+v", votes)
	}
}

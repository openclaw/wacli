package store

import (
	"reflect"
	"testing"
	"time"
)

func TestHistoricalLIDJIDsFindsChatAndMessageColumns(t *testing.T) {
	db := openTestDB(t)
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	pn := "15551234567@s.whatsapp.net"
	lid := "999123456789@lid"
	group := "120363000000@g.us"
	for _, jid := range []string{pn, lid, group} {
		if err := db.UpsertChat(jid, "dm", jid, base); err != nil {
			t.Fatalf("UpsertChat %s: %v", jid, err)
		}
	}
	if err := db.UpsertMessage(UpsertMessageParams{
		ChatJID:   lid,
		MsgID:     "lid-chat",
		SenderJID: lid,
		Timestamp: base,
		Text:      "lid chat",
	}); err != nil {
		t.Fatalf("UpsertMessage lid chat: %v", err)
	}
	if err := db.UpsertMessage(UpsertMessageParams{
		ChatJID:   group,
		MsgID:     "group-sender",
		SenderJID: lid,
		Timestamp: base,
		Text:      "group sender",
	}); err != nil {
		t.Fatalf("UpsertMessage group sender: %v", err)
	}
	if err := db.UpsertPoll(Poll{
		ChatJID:   lid,
		MsgID:     "lid-poll",
		SenderJID: lid,
		Question:  "LID?",
		Options:   []string{"yes", "no"},
		CreatedAt: base,
	}); err != nil {
		t.Fatalf("UpsertPoll lid: %v", err)
	}
	if err := db.UpsertPollVote(PollVote{
		ChatJID:   group,
		PollMsgID: "group-poll",
		VoterJID:  lid,
		VoteMsgID: "lid-vote",
		Selected:  []string{"yes"},
		VotedAt:   base,
	}); err != nil {
		t.Fatalf("UpsertPollVote lid: %v", err)
	}

	got, err := db.HistoricalLIDJIDs()
	if err != nil {
		t.Fatalf("HistoricalLIDJIDs: %v", err)
	}
	if want := []string{lid}; !reflect.DeepEqual(got, want) {
		t.Fatalf("HistoricalLIDJIDs = %#v, want %#v", got, want)
	}
}

func TestMigrateLIDToPNMergesChatsAndMessages(t *testing.T) {
	db := openTestDB(t)
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	pn := "15551234567@s.whatsapp.net"
	lid := "999123456789@lid"
	group := "120363000000@g.us"
	if err := db.UpsertChat(pn, "dm", "Alice", base); err != nil {
		t.Fatalf("UpsertChat pn: %v", err)
	}
	if err := db.UpsertChat(lid, "unknown", lid, base.Add(10*time.Second)); err != nil {
		t.Fatalf("UpsertChat lid: %v", err)
	}
	if err := db.SetChatUnreadCount(pn, 2); err != nil {
		t.Fatalf("SetChatUnreadCount pn: %v", err)
	}
	if err := db.SetChatUnreadCount(lid, 3); err != nil {
		t.Fatalf("SetChatUnreadCount lid: %v", err)
	}
	if err := db.UpsertChat(group, "group", "Project", base); err != nil {
		t.Fatalf("UpsertChat group: %v", err)
	}

	if err := db.UpsertMessage(UpsertMessageParams{
		ChatJID:   pn,
		MsgID:     "dupe",
		SenderJID: "",
		Timestamp: base,
	}); err != nil {
		t.Fatalf("UpsertMessage pn dupe: %v", err)
	}
	if err := db.UpsertMessage(UpsertMessageParams{
		ChatJID:    lid,
		ChatName:   "Alice LID",
		MsgID:      "dupe",
		SenderJID:  lid,
		SenderName: "Alice",
		Timestamp:  base.Add(5 * time.Second),
		Text:       "from lid",
	}); err != nil {
		t.Fatalf("UpsertMessage lid dupe: %v", err)
	}
	if err := db.UpsertMessage(UpsertMessageParams{
		ChatJID:    lid,
		ChatName:   "Alice LID",
		MsgID:      "lid-only",
		SenderJID:  lid,
		SenderName: "Alice",
		Timestamp:  base.Add(6 * time.Second),
		Text:       "only on lid",
	}); err != nil {
		t.Fatalf("UpsertMessage lid only: %v", err)
	}
	if err := db.UpsertMessage(UpsertMessageParams{
		ChatJID:   group,
		MsgID:     "group",
		SenderJID: lid,
		Timestamp: base.Add(7 * time.Second),
		Text:      "group message",
	}); err != nil {
		t.Fatalf("UpsertMessage group: %v", err)
	}
	if err := db.UpsertPoll(Poll{
		ChatJID:         lid,
		MsgID:           "poll",
		SenderJID:       lid,
		Question:        "Dinner?",
		Options:         []string{"yes", "no"},
		SelectableCount: 1,
		CreatedAt:       base.Add(8 * time.Second),
	}); err != nil {
		t.Fatalf("UpsertPoll lid: %v", err)
	}
	if err := db.UpsertPoll(Poll{
		ChatJID:         pn,
		MsgID:           "poll",
		SenderJID:       pn,
		Question:        "Dinner?",
		Options:         []string{"yes", "no", "maybe"},
		SelectableCount: 1,
		CreatedAt:       base.Add(9 * time.Second),
	}); err != nil {
		t.Fatalf("UpsertPoll pn: %v", err)
	}
	if err := db.UpsertPoll(Poll{
		ChatJID:         group,
		MsgID:           "group-poll",
		SenderJID:       lid,
		Question:        "Group?",
		Options:         []string{"yes", "no"},
		SelectableCount: 1,
		CreatedAt:       base.Add(8 * time.Second),
	}); err != nil {
		t.Fatalf("UpsertPoll group: %v", err)
	}
	if err := db.UpsertPollVote(PollVote{
		ChatJID:   lid,
		PollMsgID: "poll",
		VoterJID:  lid,
		VoteMsgID: "older-vote",
		Selected:  []string{"yes"},
		VotedAt:   base.Add(8 * time.Second),
	}); err != nil {
		t.Fatalf("UpsertPollVote lid: %v", err)
	}
	if err := db.UpsertPollVote(PollVote{
		ChatJID:   pn,
		PollMsgID: "poll",
		VoterJID:  pn,
		VoteMsgID: "newer-vote",
		Selected:  []string{"no"},
		VotedAt:   base.Add(9 * time.Second),
	}); err != nil {
		t.Fatalf("UpsertPollVote pn: %v", err)
	}

	if err := db.MigrateLIDToPN(lid, pn); err != nil {
		t.Fatalf("MigrateLIDToPN: %v", err)
	}
	if err := db.MigrateLIDToPN(lid, pn); err != nil {
		t.Fatalf("MigrateLIDToPN idempotent: %v", err)
	}

	if got := countRows(t, db.sql, "SELECT COUNT(*) FROM chats WHERE jid = ?", lid); got != 0 {
		t.Fatalf("lid chat rows = %d, want 0", got)
	}
	if got := countRows(t, db.sql, "SELECT COUNT(*) FROM messages WHERE chat_jid = ?", lid); got != 0 {
		t.Fatalf("lid chat message rows = %d, want 0", got)
	}
	if got := countRows(t, db.sql, "SELECT COUNT(*) FROM messages WHERE sender_jid = ?", lid); got != 0 {
		t.Fatalf("lid sender rows = %d, want 0", got)
	}
	if got := countRows(t, db.sql, "SELECT COUNT(*) FROM polls WHERE chat_jid = ? OR (sender_jid = ? AND chat_jid NOT GLOB '*@g.us')", lid, lid); got != 0 {
		t.Fatalf("lid poll rows = %d, want 0", got)
	}
	if got := countRows(t, db.sql, "SELECT COUNT(*) FROM poll_votes WHERE chat_jid = ? OR voter_jid = ?", lid, lid); got != 0 {
		t.Fatalf("lid poll vote rows = %d, want 0", got)
	}
	if got := countRows(t, db.sql, "SELECT COUNT(*) FROM messages WHERE chat_jid = ?", pn); got != 2 {
		t.Fatalf("pn message rows = %d, want 2", got)
	}

	chat, err := db.GetChat(pn)
	if err != nil {
		t.Fatalf("GetChat pn: %v", err)
	}
	if chat.Name != "Alice" {
		t.Fatalf("merged chat name = %q, want Alice", chat.Name)
	}
	if !chat.LastMessageTS.Equal(base.Add(10 * time.Second)) {
		t.Fatalf("merged chat timestamp = %s, want %s", chat.LastMessageTS, base.Add(10*time.Second))
	}
	if !chat.Unread || chat.UnreadCount != 5 {
		t.Fatalf("merged chat unread state = %+v, want unread count 5", chat)
	}

	dupe, err := db.GetMessage(pn, "dupe")
	if err != nil {
		t.Fatalf("GetMessage dupe: %v", err)
	}
	if dupe.Text != "from lid" {
		t.Fatalf("merged duplicate text = %q, want from lid", dupe.Text)
	}
	if dupe.SenderJID != pn {
		t.Fatalf("merged duplicate sender = %q, want %q", dupe.SenderJID, pn)
	}
	if !dupe.Timestamp.Equal(base.Add(5 * time.Second)) {
		t.Fatalf("merged duplicate timestamp = %s, want %s", dupe.Timestamp, base.Add(5*time.Second))
	}

	groupMsg, err := db.GetMessage(group, "group")
	if err != nil {
		t.Fatalf("GetMessage group: %v", err)
	}
	if groupMsg.SenderJID != pn {
		t.Fatalf("group sender = %q, want %q", groupMsg.SenderJID, pn)
	}

	poll, err := db.GetPoll(pn, "poll")
	if err != nil {
		t.Fatalf("GetPoll migrated: %v", err)
	}
	if poll.SenderJID != pn {
		t.Fatalf("migrated poll sender = %q, want %q", poll.SenderJID, pn)
	}
	if !reflect.DeepEqual(poll.Options, []string{"yes", "no", "maybe"}) {
		t.Fatalf("migrated poll options = %#v, want yes/no/maybe", poll.Options)
	}
	groupPoll, err := db.GetPoll(group, "group-poll")
	if err != nil {
		t.Fatalf("GetPoll group: %v", err)
	}
	if groupPoll.SenderJID != lid {
		t.Fatalf("group poll sender = %q, want %q", groupPoll.SenderJID, lid)
	}
	votes, err := db.ListPollVotes(pn, "poll")
	if err != nil {
		t.Fatalf("ListPollVotes migrated: %v", err)
	}
	if len(votes) != 1 || votes[0].VoterJID != pn || votes[0].VoteMsgID != "newer-vote" || !reflect.DeepEqual(votes[0].Selected, []string{"no"}) {
		t.Fatalf("migrated votes = %+v", votes)
	}

	lids, err := db.HistoricalLIDJIDs()
	if err != nil {
		t.Fatalf("HistoricalLIDJIDs after migrate: %v", err)
	}
	if len(lids) != 0 {
		t.Fatalf("HistoricalLIDJIDs after migrate = %#v, want none", lids)
	}
}

func TestMigrateLIDToPNPreservesButtons(t *testing.T) {
	db := openTestDB(t)
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	pn := "15551234567@s.whatsapp.net"
	lid := "999123456789@lid"
	if err := db.UpsertChat(lid, "dm", "Alice", base); err != nil {
		t.Fatalf("UpsertChat lid: %v", err)
	}

	want := []Button{
		{Type: "url", DisplayText: "Buy flights", URL: "https://example.com/flights"},
		{Type: "quick_reply", DisplayText: "No thanks", ID: "no"},
	}
	if err := db.UpsertMessage(UpsertMessageParams{
		ChatJID:   lid,
		MsgID:     "tmpl1",
		SenderJID: lid,
		Timestamp: base,
		Text:      "Check our deals",
		Buttons:   want,
	}); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}

	if err := db.MigrateLIDToPN(lid, pn); err != nil {
		t.Fatalf("MigrateLIDToPN: %v", err)
	}

	msg, err := db.GetMessage(pn, "tmpl1")
	if err != nil {
		t.Fatalf("GetMessage after migration: %v", err)
	}
	if len(msg.Buttons) != len(want) {
		t.Fatalf("expected %d buttons after migration, got %d: %+v", len(want), len(msg.Buttons), msg.Buttons)
	}
	for i, b := range want {
		got := msg.Buttons[i]
		if got.Type != b.Type || got.DisplayText != b.DisplayText || got.ID != b.ID || got.URL != b.URL {
			t.Fatalf("button[%d]: got %+v, want %+v", i, got, b)
		}
	}
}

func TestMigrateLIDToPNPreservesEditedState(t *testing.T) {
	db := openTestDB(t)
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	pn := "15551234567@s.whatsapp.net"
	lid := "999123456789@lid"
	if err := db.UpsertChat(pn, "dm", "Alice", base); err != nil {
		t.Fatalf("UpsertChat pn: %v", err)
	}
	if err := db.UpsertChat(lid, "dm", "Alice", base); err != nil {
		t.Fatalf("UpsertChat lid: %v", err)
	}
	if err := db.UpsertMessage(UpsertMessageParams{
		ChatJID:     pn,
		MsgID:       "mid",
		SenderJID:   pn,
		Timestamp:   base,
		Text:        "original",
		DisplayText: "original",
	}); err != nil {
		t.Fatalf("UpsertMessage pn original: %v", err)
	}
	if err := db.UpsertMessage(UpsertMessageParams{
		ChatJID:     lid,
		MsgID:       "mid",
		SenderJID:   lid,
		Timestamp:   base.Add(time.Minute),
		Text:        "edited",
		DisplayText: "edited",
		Edited:      true,
	}); err != nil {
		t.Fatalf("UpsertMessage lid edited: %v", err)
	}

	if err := db.MigrateLIDToPN(lid, pn); err != nil {
		t.Fatalf("MigrateLIDToPN: %v", err)
	}

	msg, err := db.GetMessage(pn, "mid")
	if err != nil {
		t.Fatalf("GetMessage after migration: %v", err)
	}
	if msg.Text != "edited" || msg.DisplayText != "edited" {
		t.Fatalf("migration lost edited body: %+v", msg)
	}
	if !msg.Timestamp.Equal(base) {
		t.Fatalf("timestamp = %s, want original timestamp", msg.Timestamp)
	}

	var edited, editedTS int64
	if err := db.sql.QueryRow(`SELECT edited, edited_ts FROM messages WHERE chat_jid = ? AND msg_id = ?`, pn, "mid").Scan(&edited, &editedTS); err != nil {
		t.Fatalf("query edited metadata: %v", err)
	}
	if edited != 1 || editedTS != base.Add(time.Minute).Unix() {
		t.Fatalf("edited metadata = (%d, %d), want (1, %d)", edited, editedTS, base.Add(time.Minute).Unix())
	}
	if err := db.UpsertMessage(UpsertMessageParams{
		ChatJID:     pn,
		MsgID:       "mid",
		SenderJID:   pn,
		Timestamp:   base,
		Text:        "original again",
		DisplayText: "original again",
	}); err != nil {
		t.Fatalf("UpsertMessage original after migration: %v", err)
	}
	msg, err = db.GetMessage(pn, "mid")
	if err != nil {
		t.Fatalf("GetMessage after original: %v", err)
	}
	if msg.Text != "edited" {
		t.Fatalf("original upsert clobbered migrated edit: %q", msg.Text)
	}
}

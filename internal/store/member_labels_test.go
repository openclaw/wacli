package store

import (
	"path/filepath"
	"testing"
	"time"
)

const (
	labelGroup = "120363000000@g.us"
	labelUser  = "15551234567@s.whatsapp.net"
)

func labelAt(day int) time.Time {
	return time.Date(2026, 11, day, 12, 0, 0, 0, time.UTC)
}

func recordLabel(t *testing.T, db *DB, p RecordGroupMemberLabelParams) {
	t.Helper()
	if p.GroupJID == "" {
		p.GroupJID = labelGroup
	}
	if p.UserJID == "" {
		p.UserJID = labelUser
	}
	if p.Source == "" {
		p.Source = MemberLabelSourceMessage
	}
	if err := db.RecordGroupMemberLabel(p); err != nil {
		t.Fatalf("RecordGroupMemberLabel: %v", err)
	}
}

type labelRow struct {
	label  string
	setAt  time.Time
	source string
}

func requireLabelHistory(t *testing.T, db *DB, want ...labelRow) {
	t.Helper()
	got, err := db.ListGroupMemberLabels(labelGroup, labelUser)
	if err != nil {
		t.Fatalf("ListGroupMemberLabels: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("history = %+v, want %d rows", got, len(want))
	}
	for i, w := range want {
		if got[i].Label != w.label || !got[i].SetAt.Equal(w.setAt) || got[i].Source != w.source {
			t.Fatalf("row %d = %+v, want %+v", i, got[i], w)
		}
	}
}

// Every message repeats the sender's label; only a change becomes a row.
func TestRecordGroupMemberLabelKeepsOneRowPerChange(t *testing.T) {
	db := openTestDB(t)
	for day := 1; day <= 3; day++ {
		recordLabel(t, db, RecordGroupMemberLabelParams{Label: "Mom of Alex", SeenAt: labelAt(day)})
	}
	recordLabel(t, db, RecordGroupMemberLabelParams{Label: "Mom of Alex & Ben", SeenAt: labelAt(5)})
	recordLabel(t, db, RecordGroupMemberLabelParams{Label: "Mom of Alex & Ben", SeenAt: labelAt(6)})
	requireLabelHistory(t, db,
		labelRow{"Mom of Alex", labelAt(1), MemberLabelSourceMessage},
		labelRow{"Mom of Alex & Ben", labelAt(5), MemberLabelSourceMessage},
	)
}

// History sync hands over messages newest first; the result must match the
// chronological order instead of adding a row per older message.
func TestRecordGroupMemberLabelOutOfOrder(t *testing.T) {
	db := openTestDB(t)
	for day := 6; day >= 1; day-- {
		label := "Coach"
		if day <= 3 {
			label = "Mom of Alex"
		}
		recordLabel(t, db, RecordGroupMemberLabelParams{Label: label, SeenAt: labelAt(day)})
	}
	requireLabelHistory(t, db,
		labelRow{"Mom of Alex", labelAt(1), MemberLabelSourceMessage},
		labelRow{"Coach", labelAt(4), MemberLabelSourceMessage},
	)
}

// An exact change time replaces the estimate taken from the first message.
func TestRecordGroupMemberLabelExactTimeMovesEstimate(t *testing.T) {
	db := openTestDB(t)
	recordLabel(t, db, RecordGroupMemberLabelParams{Label: "Coach", SeenAt: labelAt(5)})
	recordLabel(t, db, RecordGroupMemberLabelParams{Label: "Coach", Source: MemberLabelSourceChange, SetAt: labelAt(2)})
	recordLabel(t, db, RecordGroupMemberLabelParams{Label: "Coach", Source: MemberLabelSourceHistorySync, SetAt: labelAt(2)})
	requireLabelHistory(t, db, labelRow{"Coach", labelAt(2), MemberLabelSourceChange})
}

// The participant list shows the newest label; a cleared label shows none
// while the history keeps it.
func TestListGroupParticipantsShowsCurrentLabel(t *testing.T) {
	db := openTestDB(t)
	other := "15559876543@s.whatsapp.net"
	if err := db.UpsertGroup(labelGroup, "Team", "", time.Time{}); err != nil {
		t.Fatalf("UpsertGroup: %v", err)
	}
	if err := db.ReplaceGroupParticipants(labelGroup, []GroupParticipant{
		{GroupJID: labelGroup, UserJID: labelUser, Role: "member", UpdatedAt: labelAt(1)},
		{GroupJID: labelGroup, UserJID: other, Role: "admin", UpdatedAt: labelAt(1)},
	}); err != nil {
		t.Fatalf("ReplaceGroupParticipants: %v", err)
	}
	recordLabel(t, db, RecordGroupMemberLabelParams{Label: "Mom of Alex", Source: MemberLabelSourceHistorySync, SetAt: labelAt(1)})
	recordLabel(t, db, RecordGroupMemberLabelParams{Label: "Coach", Source: MemberLabelSourceChange, SetAt: labelAt(3)})
	recordLabel(t, db, RecordGroupMemberLabelParams{UserJID: other, Label: "Dad of Ben", SetAt: labelAt(2)})
	recordLabel(t, db, RecordGroupMemberLabelParams{UserJID: other, Label: "", Source: MemberLabelSourceChange, SetAt: labelAt(4)})

	participants, err := db.ListGroupParticipants(labelGroup)
	if err != nil {
		t.Fatalf("ListGroupParticipants: %v", err)
	}
	labels := map[string]GroupParticipant{}
	for _, p := range participants {
		labels[p.UserJID] = p
	}
	if p := labels[labelUser]; p.Label != "Coach" || p.LabelSetAt == nil || !p.LabelSetAt.Equal(labelAt(3)) {
		t.Fatalf("participant = %+v, want current label Coach", p)
	}
	if p := labels[other]; p.Label != "" || p.LabelSetAt != nil {
		t.Fatalf("participant = %+v, want the cleared label to show none", p)
	}
	history, err := db.ListGroupMemberLabels(labelGroup, other)
	if err != nil || len(history) != 2 || history[1].Label != "" {
		t.Fatalf("history = %+v, err = %v; want the label and its clearing", history, err)
	}
}

// Labels follow a hidden-user identity to its phone number like other rows.
func TestMigrateLIDToPNMovesMemberLabels(t *testing.T) {
	db := openTestDB(t)
	lid := "999123456789@lid"
	recordLabel(t, db, RecordGroupMemberLabelParams{UserJID: lid, Label: "Coach", SetAt: labelAt(2)})
	lids, err := db.HistoricalLIDJIDs()
	if err != nil || len(lids) != 1 || lids[0] != lid {
		t.Fatalf("HistoricalLIDJIDs = %v, err = %v; want the labelled lid", lids, err)
	}
	if err := db.MigrateLIDToPN(lid, labelUser); err != nil {
		t.Fatalf("MigrateLIDToPN: %v", err)
	}
	requireLabelHistory(t, db, labelRow{"Coach", labelAt(2), MemberLabelSourceMessage})
	if got := countRows(t, db.sql, "SELECT COUNT(*) FROM group_member_labels WHERE user_jid = ?", lid); got != 0 {
		t.Fatalf("lid label rows = %d, want 0", got)
	}
}

func TestDeleteGroupLocalDataDeletesMemberLabels(t *testing.T) {
	db := openTestDB(t)
	recordLabel(t, db, RecordGroupMemberLabelParams{Label: "Coach", SetAt: labelAt(2)})
	if err := db.DeleteGroupLocalData(labelGroup); err != nil {
		t.Fatalf("DeleteGroupLocalData: %v", err)
	}
	if got := countRows(t, db.sql, "SELECT COUNT(*) FROM group_member_labels"); got != 0 {
		t.Fatalf("label rows = %d, want 0", got)
	}
}

// A store from before labels gets the table on the next writable open, and a
// read-only open of such a store answers with no labels instead of failing.
func TestOlderStoreGetsMemberLabelTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wacli.db")
	db := mustOpen(t, path)
	if err := db.UpsertGroup(labelGroup, "Team", "", time.Time{}); err != nil {
		t.Fatalf("UpsertGroup: %v", err)
	}
	if err := db.ReplaceGroupParticipants(labelGroup, []GroupParticipant{
		{GroupJID: labelGroup, UserJID: labelUser, Role: "member", UpdatedAt: labelAt(1)},
	}); err != nil {
		t.Fatalf("ReplaceGroupParticipants: %v", err)
	}
	if _, err := db.sql.Exec(`DROP TABLE group_member_labels`); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	if _, err := db.sql.Exec(`DELETE FROM schema_migrations WHERE name = 'group member labels'`); err != nil {
		t.Fatalf("forget the migration: %v", err)
	}
	db.Close()

	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	participants, err := ro.ListGroupParticipants(labelGroup)
	if err != nil || len(participants) != 1 || participants[0].Label != "" {
		t.Fatalf("participants = %+v, err = %v; want one member without label", participants, err)
	}
	history, err := ro.ListGroupMemberLabels(labelGroup, "")
	if err != nil || len(history) != 0 {
		t.Fatalf("history = %+v, err = %v; want none", history, err)
	}
	ro.Close()

	reopened := mustOpen(t, path)
	defer reopened.Close()
	if !reopened.memberLabelsEnabled {
		t.Fatal("reopening an older store did not add the label table")
	}
	recordLabel(t, reopened, RecordGroupMemberLabelParams{Label: "Coach", SetAt: labelAt(2)})
	requireLabelHistory(t, reopened, labelRow{"Coach", labelAt(2), MemberLabelSourceMessage})
}

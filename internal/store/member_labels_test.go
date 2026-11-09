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

func labelDay(day int) time.Time {
	return time.Date(2026, 11, day, 12, 0, 0, 0, time.UTC)
}

func recordLabel(t *testing.T, db *DB, user, label string, at time.Time) {
	t.Helper()
	if err := db.RecordGroupMemberLabel(RecordGroupMemberLabelParams{GroupJID: labelGroup, UserJID: user, Label: label, At: at}); err != nil {
		t.Fatalf("RecordGroupMemberLabel: %v", err)
	}
}

func seedLabelGroup(t *testing.T, db *DB, users ...string) {
	t.Helper()
	if err := db.UpsertGroup(labelGroup, "Team", "", time.Time{}); err != nil {
		t.Fatalf("UpsertGroup: %v", err)
	}
	var ps []GroupParticipant
	for _, u := range users {
		ps = append(ps, GroupParticipant{GroupJID: labelGroup, UserJID: u, Role: "member", UpdatedAt: labelDay(1)})
	}
	if err := db.ReplaceGroupParticipants(labelGroup, ps); err != nil {
		t.Fatalf("ReplaceGroupParticipants: %v", err)
	}
}

func participantLabel(t *testing.T, db *DB, user string) GroupParticipant {
	t.Helper()
	ps, err := db.ListGroupParticipants(labelGroup)
	if err != nil {
		t.Fatalf("ListGroupParticipants: %v", err)
	}
	for _, p := range ps {
		if p.UserJID == user {
			return p
		}
	}
	t.Fatalf("participant %s not listed in %+v", user, ps)
	return GroupParticipant{}
}

func requireLabel(t *testing.T, db *DB, user, label string, at time.Time) {
	t.Helper()
	p := participantLabel(t, db, user)
	if p.Label != label {
		t.Fatalf("label = %q, want %q", p.Label, label)
	}
	switch {
	case at.IsZero() && p.LabelUpdatedAt != nil:
		t.Fatalf("label_updated_at = %v, want none", p.LabelUpdatedAt)
	case !at.IsZero() && (p.LabelUpdatedAt == nil || !p.LabelUpdatedAt.Equal(at)):
		t.Fatalf("label_updated_at = %v, want %v", p.LabelUpdatedAt, at)
	}
}

type labelObservation struct {
	label string
	at    time.Time
}

func permutations(in []labelObservation) [][]labelObservation {
	if len(in) <= 1 {
		return [][]labelObservation{append([]labelObservation(nil), in...)}
	}
	var out [][]labelObservation
	for i := range in {
		rest := append(append([]labelObservation(nil), in[:i]...), in[i+1:]...)
		for _, p := range permutations(rest) {
			out = append(out, append([]labelObservation{in[i]}, p...))
		}
	}
	return out
}

// History sync is not chronological. Whatever order the observations arrive
// in, the newest dated one wins — including a label that returns after an
// intervening one.
func TestRecordGroupMemberLabelIsOrderIndependent(t *testing.T) {
	observations := []labelObservation{
		{"Mom of Alex", labelDay(1)},
		{"Coach", labelDay(3)},
		{"Mom of Alex", labelDay(5)},
		{"Mom of Alex", time.Time{}}, // undated roster label
	}
	for i, order := range permutations(observations) {
		db := openTestDB(t)
		seedLabelGroup(t, db, labelUser)
		for _, o := range order {
			recordLabel(t, db, labelUser, o.label, o.at)
		}
		p := participantLabel(t, db, labelUser)
		if p.Label != "Mom of Alex" || p.LabelUpdatedAt == nil || !p.LabelUpdatedAt.Equal(labelDay(5)) {
			t.Fatalf("order %d %+v: participant = %+v, want Mom of Alex from day 5", i, order, p)
		}
	}
}

// A roster label with no timestamp cannot say how old it is, so it must not
// replace a label known from a dated message; it only fills a gap.
func TestUndatedMemberLabelOnlyFillsAGap(t *testing.T) {
	db := openTestDB(t)
	seedLabelGroup(t, db, labelUser)
	recordLabel(t, db, labelUser, "Mom of Alex", time.Time{})
	requireLabel(t, db, labelUser, "Mom of Alex", time.Time{})
	recordLabel(t, db, labelUser, "Coach", labelDay(3))
	recordLabel(t, db, labelUser, "Mom of Alex", time.Time{})
	requireLabel(t, db, labelUser, "Coach", labelDay(3))
}

// A newer clearing hides the label; an older message cannot bring it back.
func TestClearedMemberLabel(t *testing.T) {
	db := openTestDB(t)
	seedLabelGroup(t, db, labelUser)
	recordLabel(t, db, labelUser, "Coach", labelDay(3))
	recordLabel(t, db, labelUser, "", labelDay(4))
	recordLabel(t, db, labelUser, "Coach", labelDay(2))
	requireLabel(t, db, labelUser, "", time.Time{})
}

// Labels follow a hidden-user identity to its phone number like other rows,
// and the newer of the two survives the merge.
func TestMigrateLIDToPNMovesMemberLabels(t *testing.T) {
	db := openTestDB(t)
	lid := "999123456789@lid"
	seedLabelGroup(t, db, labelUser)
	recordLabel(t, db, labelUser, "Mom of Alex", labelDay(1))
	recordLabel(t, db, lid, "Coach", labelDay(3))
	lids, err := db.HistoricalLIDJIDs()
	if err != nil || len(lids) != 1 || lids[0] != lid {
		t.Fatalf("HistoricalLIDJIDs = %v, err = %v; want the labelled lid", lids, err)
	}
	if err := db.MigrateLIDToPN(lid, labelUser); err != nil {
		t.Fatalf("MigrateLIDToPN: %v", err)
	}
	requireLabel(t, db, labelUser, "Coach", labelDay(3))
	if got := countRows(t, db.sql, "SELECT COUNT(*) FROM group_member_labels WHERE user_jid = ?", lid); got != 0 {
		t.Fatalf("lid label rows = %d, want 0", got)
	}
}

func TestDeleteGroupLocalDataDeletesMemberLabels(t *testing.T) {
	db := openTestDB(t)
	recordLabel(t, db, labelUser, "Coach", labelDay(2))
	if err := db.DeleteGroupLocalData(labelGroup); err != nil {
		t.Fatalf("DeleteGroupLocalData: %v", err)
	}
	if got := countRows(t, db.sql, "SELECT COUNT(*) FROM group_member_labels"); got != 0 {
		t.Fatalf("label rows = %d, want 0", got)
	}
}

// A participant refresh replaces the roster but keeps the labels.
func TestParticipantRefreshKeepsMemberLabels(t *testing.T) {
	db := openTestDB(t)
	seedLabelGroup(t, db, labelUser)
	recordLabel(t, db, labelUser, "Coach", labelDay(2))
	seedLabelGroup(t, db, labelUser)
	requireLabel(t, db, labelUser, "Coach", labelDay(2))
}

// A store from before labels gets the table on the next writable open, and a
// read-only open of such a store lists members without labels instead of failing.
func TestOlderStoreGetsMemberLabelTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wacli.db")
	db := mustOpen(t, path)
	seedLabelGroup(t, db, labelUser)
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
	ps, err := ro.ListGroupParticipants(labelGroup)
	if err != nil || len(ps) != 1 || ps[0].Label != "" {
		t.Fatalf("participants = %+v, err = %v; want one member without label", ps, err)
	}
	ro.Close()

	reopened := mustOpen(t, path)
	defer reopened.Close()
	if !reopened.memberLabelsEnabled {
		t.Fatal("reopening an older store did not add the label table")
	}
	recordLabel(t, reopened, labelUser, "Coach", labelDay(2))
	requireLabel(t, reopened, labelUser, "Coach", labelDay(2))
}

// WhatsApp dates label changes to the millisecond. Two changes within one
// second must resolve to the later one in either arrival order, including a
// clear right after a set.
func TestMemberLabelOrderKeepsMilliseconds(t *testing.T) {
	second := time.UnixMilli(1767225600000).UTC()
	early, late := second.Add(100*time.Millisecond), second.Add(900*time.Millisecond)
	cases := []struct {
		name         string
		first, later labelObservation
		want         string
	}{
		{"newer label wins", labelObservation{"Z", early}, labelObservation{"A", late}, "A"},
		{"clear after set wins", labelObservation{"Coach", early}, labelObservation{"", late}, ""},
	}
	for _, tc := range cases {
		for _, order := range [][]labelObservation{{tc.first, tc.later}, {tc.later, tc.first}} {
			db := openTestDB(t)
			seedLabelGroup(t, db, labelUser)
			for _, o := range order {
				recordLabel(t, db, labelUser, o.label, o.at)
			}
			p := participantLabel(t, db, labelUser)
			if p.Label != tc.want {
				t.Fatalf("%s, order %+v: label = %q, want %q", tc.name, order, p.Label, tc.want)
			}
			if tc.want != "" && (p.LabelUpdatedAt == nil || !p.LabelUpdatedAt.Equal(late)) {
				t.Fatalf("%s: label_updated_at = %v, want %v", tc.name, p.LabelUpdatedAt, late)
			}
		}
	}
}

// Merging a hidden-user identity keeps the later label even within a second.
func TestMigrateLIDToPNKeepsMillisecondOrder(t *testing.T) {
	second := time.UnixMilli(1767225600000).UTC()
	lid := "999123456789@lid"
	for _, lidIsLater := range []bool{true, false} {
		db := openTestDB(t)
		seedLabelGroup(t, db, labelUser)
		pnAt, lidAt, want := second.Add(900*time.Millisecond), second.Add(100*time.Millisecond), "A"
		if lidIsLater {
			pnAt, lidAt, want = second.Add(100*time.Millisecond), second.Add(900*time.Millisecond), "Z"
		}
		pnLabel, lidLabel := "A", "Z"
		recordLabel(t, db, labelUser, pnLabel, pnAt)
		recordLabel(t, db, lid, lidLabel, lidAt)
		if err := db.MigrateLIDToPN(lid, labelUser); err != nil {
			t.Fatalf("MigrateLIDToPN: %v", err)
		}
		if p := participantLabel(t, db, labelUser); p.Label != want {
			t.Fatalf("lid later = %v: label = %q, want %q", lidIsLater, p.Label, want)
		}
	}
}

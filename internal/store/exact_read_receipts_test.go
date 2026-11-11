package store

import (
	"errors"
	"slices"
	"testing"
	"time"
)

type exactFixture struct {
	t    *testing.T
	db   *DB
	chat string
	at   time.Time
}

func newExactFixture(t *testing.T) *exactFixture {
	t.Helper()
	db := openTestDB(t)
	chat := "123@s.whatsapp.net"
	if err := db.UpsertChat(chat, "dm", "Alice", time.Now()); err != nil {
		t.Fatal(err)
	}
	return &exactFixture{t: t, db: db, chat: chat, at: time.Date(2024, 2, 1, 12, 0, 0, 0, time.UTC)}
}

// add stores an incoming message second seconds after the fixture start.
func (f *exactFixture) add(id string, second int, apply func(*UpsertMessageParams)) {
	f.t.Helper()
	p := UpsertMessageParams{ChatJID: f.chat, MsgID: id, SenderJID: f.chat, Timestamp: f.at.Add(time.Duration(second) * time.Second), Text: id}
	if apply != nil {
		apply(&p)
	}
	if err := f.db.UpsertMessage(p); err != nil {
		f.t.Fatalf("UpsertMessage %s: %v", id, err)
	}
}

// arrive stores a live incoming message and counts it, as sync does.
func (f *exactFixture) arrive(id string, second int) {
	f.t.Helper()
	f.add(id, second, nil)
	if err := f.db.IncrementChatUnread(f.chat); err != nil {
		f.t.Fatal(err)
	}
}

func (f *exactFixture) setUnread(n int) {
	f.t.Helper()
	if err := f.db.SetChatUnreadCount(f.chat, n); err != nil {
		f.t.Fatal(err)
	}
}

func (f *exactFixture) chatState() Chat {
	f.t.Helper()
	c, err := f.db.GetChat(f.chat)
	if err != nil {
		f.t.Fatal(err)
	}
	return c
}

// unreadSelection lists the messages wacli's positional model treats as
// unread: the newest unread_count eligible rows, oldest first.
func (f *exactFixture) unreadSelection() []string {
	f.t.Helper()
	c := f.chatState()
	rows, err := f.db.sql.Query("SELECT msg_id FROM (SELECT msg_id, ts, rowid FROM messages WHERE "+receiptEligible+" ORDER BY ts DESC, rowid DESC LIMIT ?) ORDER BY ts ASC, rowid ASC", f.chat, c.UnreadCount)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			f.t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

// contentless stores a message as sync does when it could not read its
// content yet: a "(message)" placeholder that may still be unread.
func contentless(p *UpsertMessageParams) { p.Text, p.DisplayText = "", "(message)" }

func msgIDs(msgs []MessageInfo) []string {
	var ids []string
	for _, m := range msgs {
		ids = append(ids, m.MsgID)
	}
	return ids
}

func TestExactReceiptsSelectOnlyTheOldestUnreadMessages(t *testing.T) {
	f := newExactFixture(t)
	f.add("read-before", 0, nil)
	f.add("a", 10, nil)
	f.add("mine", 15, func(p *UpsertMessageParams) { p.FromMe = true })
	f.add("reaction", 16, func(p *UpsertMessageParams) { p.ReactionToID, p.ReactionEmoji = "a", "x" })
	f.add("b", 20, nil)
	f.add("c", 30, nil)
	f.setUnread(3)

	msgs, snap, err := f.db.ReadReceiptCandidatesExact(f.chat, []string{"b", "a"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(msgIDs(msgs), []string{"a", "b"}) || len(snap.RowIDs) != 2 || !snap.HasFloor {
		t.Fatalf("selection = %v %+v, want rows a,b oldest first above the read floor", msgIDs(msgs), snap)
	}

	for _, tc := range []struct {
		name string
		ids  []string
	}{
		{"skips an older unread message", []string{"b"}},
		{"leaves a gap between requested messages", []string{"a", "c"}},
		{"already read locally", []string{"read-before"}},
		{"more than are unread", []string{"a", "b", "c", "read-before"}},
		{"outgoing", []string{"mine"}},
		{"reaction", []string{"reaction"}},
		{"unknown", []string{"missing"}},
	} {
		if _, _, err := f.db.ReadReceiptCandidatesExact(f.chat, tc.ids); !errors.Is(err, ErrNotOldestUnread) {
			t.Fatalf("%s: err = %v, want ErrNotOldestUnread", tc.name, err)
		}
	}
	for _, ids := range [][]string{nil, {""}, {" a"}, {"a", "a"}} {
		if _, _, err := f.db.ReadReceiptCandidatesExact(f.chat, ids); err == nil {
			t.Fatalf("invalid IDs %q accepted", ids)
		}
	}
	if _, _, err := f.db.ReadReceiptCandidatesExact("999@s.whatsapp.net", []string{"a"}); err == nil {
		t.Fatal("unknown chat accepted")
	}
}

func TestExactReceiptsRefuseMarkerOnlyAndIncompleteHistory(t *testing.T) {
	f := newExactFixture(t)
	f.add("a", 10, nil)
	if err := f.db.SetChatUnread(f.chat, true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.db.ReadReceiptCandidatesExact(f.chat, []string{"a"}); !errors.Is(err, ErrNotOldestUnread) {
		t.Fatalf("marker-only chat: err = %v, want ErrNotOldestUnread", err)
	}
	f.setUnread(3)
	if _, _, err := f.db.ReadReceiptCandidatesExact(f.chat, []string{"a"}); err == nil || errors.Is(err, ErrNotOldestUnread) {
		t.Fatalf("partial history: err = %v, want the history guard", err)
	}
}

// A message without readable content after the last read one may be unread,
// and its content can still arrive and be counted. Receipts for later messages
// could cover it, and settlement would leave it read, so nothing is sent.
func TestExactReceiptsRefuseAContentlessMessageInTheirRange(t *testing.T) {
	for _, tc := range []struct {
		name   string
		second int
		ok     bool
	}{
		{"between the acknowledged messages", 20, false},
		{"between the last read message and them", 7, false},
		{"before the last read message", 1, true},
		{"after them", 40, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newExactFixture(t)
			f.add("read", 5, nil)
			f.add("a", 10, nil)
			f.add("b", 30, nil)
			f.add("p", tc.second, contentless)
			f.setUnread(2)
			_, _, err := f.db.ReadReceiptCandidatesExact(f.chat, []string{"a", "b"})
			if tc.ok && err != nil {
				t.Fatalf("refused: %v", err)
			}
			if !tc.ok && !errors.Is(err, ErrNotOldestUnread) {
				t.Fatalf("err = %v, want ErrNotOldestUnread", err)
			}
		})
	}
}

func TestExactReceiptsUseRowOrderWithinOneSecond(t *testing.T) {
	f := newExactFixture(t)
	f.add("first", 10, nil)
	f.add("second", 10, nil)
	f.setUnread(2)
	if _, _, err := f.db.ReadReceiptCandidatesExact(f.chat, []string{"second"}); !errors.Is(err, ErrNotOldestUnread) {
		t.Fatalf("later same-second row accepted before the earlier one: %v", err)
	}
	msgs, _, err := f.db.ReadReceiptCandidatesExact(f.chat, []string{"first"})
	if err != nil || !slices.Equal(msgIDs(msgs), []string{"first"}) {
		t.Fatalf("earlier same-second row: %v %v", msgIDs(msgs), err)
	}
}

// settle runs one exact acknowledgment: select, then let during() change the
// store while receipts would be in flight, then settle.
func settleExact(t *testing.T, f *exactFixture, ids []string, during func()) bool {
	t.Helper()
	_, snap, err := f.db.ReadReceiptCandidatesExact(f.chat, ids)
	if err != nil {
		t.Fatal(err)
	}
	if during != nil {
		during()
	}
	settled, err := f.db.SettleExactReceipts(f.chat, snap)
	if err != nil {
		t.Fatal(err)
	}
	return settled
}

func TestExactSettlementKeepsNewerArrivalsUnread(t *testing.T) {
	f := newExactFixture(t)
	f.add("a", 10, nil)
	f.add("b", 30, nil)
	f.add("c", 40, nil)
	f.setUnread(3)
	if !settleExact(t, f, []string{"a", "b"}, func() {
		f.arrive("newer", 50)
		f.arrive("same-second-later", 30) // stored after b in the same second
	}) {
		t.Fatal("clean dispatch with only newer arrivals was not settled")
	}
	if got := f.unreadSelection(); !slices.Equal(got, []string{"same-second-later", "c", "newer"}) || !f.chatState().Unread {
		t.Fatalf("unread after settlement = %v, want only the messages after b", got)
	}
}

// Race: read A(10) and B(30) are selected; unseen X(20)
// arrives while receipts are in flight. The positional clear drops X with the
// expected receipt count; exact settlement must keep it unread.
func TestExactSettlementKeepsAnOlderArrivalDuringDispatch(t *testing.T) {
	f := newExactFixture(t)
	f.add("a", 10, nil)
	f.add("b", 30, nil)
	f.setUnread(2)
	if settleExact(t, f, []string{"a", "b"}, func() { f.arrive("x", 20) }) {
		t.Fatal("settled although an older message arrived during dispatch")
	}
	if got := f.unreadSelection(); !slices.Contains(got, "x") || f.chatState().UnreadCount != 3 {
		t.Fatalf("unread after settlement = %v (count %d), want x kept", got, f.chatState().UnreadCount)
	}

	// The same arrival through the existing positional clear, for contrast.
	g := newExactFixture(t)
	g.add("a", 10, nil)
	g.add("b", 30, nil)
	g.setUnread(2)
	g.arrive("x", 20)
	if err := g.db.ClearChatUnreadThroughPreservingMarker(g.chat, g.at.Add(30*time.Second), []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if got := g.chatState().UnreadCount; got != 0 {
		t.Fatalf("positional clear left %d unread; the contrast case no longer reproduces", got)
	}
}

func TestExactSettlementKeepsOlderSameSecondAndStoredButUncountedArrivals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		during func(*exactFixture)
		// pending completes a count that sync had not applied at settlement.
		pending bool
		// kept is false where wacli's newest-N count cannot hold x either way.
		kept bool
	}{
		// Stored in a's second after a, so positioned between a and b.
		{"same second as a", func(f *exactFixture) { f.arrive("x", 10) }, false, true},
		// Sync stores the row before it increments the count.
		{"stored, not yet counted", func(f *exactFixture) { f.add("x", 20, nil) }, true, true},
		// Older than every unread message and an already read one.
		{"older than read history", func(f *exactFixture) { f.arrive("x", 1) }, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newExactFixture(t)
			f.add("read", 5, nil)
			f.add("a", 10, nil)
			f.add("b", 30, nil)
			f.setUnread(2)
			before := f.chatState().UnreadCount
			if settleExact(t, f, []string{"a", "b"}, func() { tc.during(f) }) {
				t.Fatal("settled although the chat changed at or before the acknowledged messages")
			}
			if got := f.chatState().UnreadCount; got < before {
				t.Fatalf("unread count dropped from %d to %d", before, got)
			}
			if tc.pending {
				if err := f.db.IncrementChatUnread(f.chat); err != nil {
					t.Fatal(err)
				}
			}
			if got := f.unreadSelection(); tc.kept && !slices.Contains(got, "x") {
				t.Fatalf("unread after the arrival was counted = %v, want x", got)
			}
		})
	}
}

// Offsetting changes during dispatch keep every count
// equal. a is revoked, unseen x is stored between a and b, and sync counts x
// only after settlement. The range must still hold exactly a and b by row, so
// x stays unread once its count lands.
func TestExactSettlementRejectsOffsettingRevokeAndUncountedArrival(t *testing.T) {
	for _, tc := range []struct {
		name   string
		during func(*exactFixture)
		// pending completes a count that sync had not applied at settlement.
		pending bool
	}{
		{"revoke, then an uncounted arrival", func(f *exactFixture) {
			if err := f.db.MarkMessageRevoked(f.chat, "a"); err != nil {
				f.t.Fatal(err)
			}
			f.add("x", 20, nil)
		}, true},
		{"revoke, a counted arrival and a lower count elsewhere", func(f *exactFixture) {
			if err := f.db.MarkMessageRevoked(f.chat, "a"); err != nil {
				f.t.Fatal(err)
			}
			f.arrive("x", 20)
			f.setUnread(2)
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newExactFixture(t)
			f.add("a", 10, nil)
			f.add("b", 30, nil)
			f.setUnread(2)
			if settleExact(t, f, []string{"a", "b"}, func() { tc.during(f) }) {
				t.Fatal("settled although the acknowledged range changed")
			}
			if got := f.chatState().UnreadCount; got != 2 {
				t.Fatalf("unread count = %d, want 2 kept", got)
			}
			if tc.pending {
				if err := f.db.IncrementChatUnread(f.chat); err != nil {
					t.Fatal(err)
				}
			}
			if got := f.unreadSelection(); !slices.Contains(got, "x") {
				t.Fatalf("unread after the arrival was counted = %v, want x", got)
			}
		})
	}
}

// A contentless message stored in the acknowledged range during dispatch is
// not readable, so it is not among the rows compared. Its content can still
// arrive and be counted; settlement must not have marked its place read.
func TestExactSettlementKeepsAContentlessArrivalInItsRange(t *testing.T) {
	f := newExactFixture(t)
	f.add("a", 10, nil)
	f.add("b", 30, nil)
	f.setUnread(2)
	if settleExact(t, f, []string{"a", "b"}, func() { f.add("p", 20, contentless) }) {
		t.Fatal("settled with a contentless message in the acknowledged range")
	}
	f.add("p", 20, nil) // its content arrives
	if err := f.db.IncrementChatUnread(f.chat); err != nil {
		t.Fatal(err)
	}
	if got := f.unreadSelection(); !slices.Contains(got, "p") {
		t.Fatalf("unread after p's content arrived = %v, want p", got)
	}
}

// A read elsewhere during dispatch already lowered the count, and a later
// arrival older than b then raised it again: with the same count, the old
// positional model would attribute x's unit to b. Settlement must not take it.
func TestExactSettlementDoesNotTakeAnArrivalsUnitAfterAReadElsewhere(t *testing.T) {
	f := newExactFixture(t)
	f.add("b", 30, nil)
	f.setUnread(1)
	if settleExact(t, f, []string{"b"}, func() {
		if err := f.db.ClearChatUnreadThrough(f.chat, f.at.Add(30*time.Second), []string{"b"}); err != nil {
			t.Fatal(err)
		}
		f.arrive("x", 20)
	}) {
		t.Fatal("settled after a read elsewhere and an older arrival")
	}
	if got := f.chatState().UnreadCount; got != 1 {
		t.Fatalf("unread count = %d, want x's arrival kept", got)
	}

	// The same read elsewhere followed by a newer arrival: also kept.
	g := newExactFixture(t)
	g.add("b", 30, nil)
	g.setUnread(1)
	if settleExact(t, g, []string{"b"}, func() {
		if err := g.db.ClearChatUnreadThrough(g.chat, g.at.Add(30*time.Second), []string{"b"}); err != nil {
			t.Fatal(err)
		}
		g.arrive("y", 40)
	}) {
		t.Fatal("settled after a read elsewhere and a newer arrival")
	}
	if got := g.unreadSelection(); !slices.Equal(got, []string{"y"}) {
		t.Fatalf("unread = %v, want y kept", got)
	}
}

func TestExactSettlementLeavesChangedOrRepeatedStateAlone(t *testing.T) {
	f := newExactFixture(t)
	f.add("a", 10, nil)
	f.add("b", 30, nil)
	f.setUnread(2)
	_, snap, err := f.db.ReadReceiptCandidatesExact(f.chat, []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	// Two helpers acknowledging the same messages settle once, never twice.
	for i, want := range []bool{true, false} {
		settled, err := f.db.SettleExactReceipts(f.chat, snap)
		if err != nil || settled != want {
			t.Fatalf("settlement %d = %t, %v; want %t", i, settled, err, want)
		}
	}
	if c := f.chatState(); c.UnreadCount != 0 || c.Unread {
		t.Fatalf("after settlement = %+v, want read", c)
	}

	// A message revoked during dispatch changes the acknowledged range.
	g := newExactFixture(t)
	g.add("a", 10, nil)
	g.add("b", 30, nil)
	g.setUnread(2)
	if settleExact(t, g, []string{"a", "b"}, func() {
		if err := g.db.MarkMessageRevoked(g.chat, "a"); err != nil {
			t.Fatal(err)
		}
	}) {
		t.Fatal("settled after an acknowledged message was revoked")
	}

	// The count already cleared elsewhere stays cleared, marker untouched.
	h := newExactFixture(t)
	h.add("a", 10, nil)
	h.setUnread(1)
	if settleExact(t, h, []string{"a"}, func() {
		if err := h.db.SetChatUnreadCount(h.chat, 0); err != nil {
			t.Fatal(err)
		}
		if err := h.db.SetChatUnread(h.chat, true); err != nil {
			t.Fatal(err)
		}
	}) {
		t.Fatal("settled a chat whose count was already cleared")
	}
	if c := h.chatState(); c.UnreadCount != 0 || !c.Unread {
		t.Fatalf("marker changed: %+v", c)
	}
	if _, err := h.db.SettleExactReceipts(h.chat, ExactReceiptSnapshot{}); err == nil {
		t.Fatal("empty snapshot accepted")
	}
}

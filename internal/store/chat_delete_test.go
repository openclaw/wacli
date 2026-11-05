package store

import (
	"testing"
	"time"
)

func TestChatDeleteRangeNamesTheNewestMessageAtOrBeforeThrough(t *testing.T) {
	db := openTestDB(t)
	chat := "15550001111@s.whatsapp.net"
	if err := db.UpsertChat(chat, "dm", "Nora Vitale", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	through := time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)
	add := func(id string, at time.Time, apply func(*UpsertMessageParams)) {
		t.Helper()
		p := UpsertMessageParams{ChatJID: chat, MsgID: id, SenderJID: chat, Timestamp: at, Text: "text " + id}
		if apply != nil {
			apply(&p)
		}
		if err := db.UpsertMessage(p); err != nil {
			t.Fatalf("UpsertMessage %s: %v", id, err)
		}
	}
	add("older", through.Add(-time.Hour), nil)
	add("mine", through.Add(-time.Minute), func(p *UpsertMessageParams) { p.FromMe = true; p.SenderJID = "" })
	// A reaction belongs to its message: never the range's last message.
	add("reaction", through.Add(-time.Second), func(p *UpsertMessageParams) {
		p.ReactionToID = "older"
		p.ReactionEmoji = "👍"
	})

	// through carries a fraction of a second: the store's whole seconds at
	// its second are at or before it.
	r, err := db.ChatDeleteRange([]string{chat}, through.Add(500*time.Millisecond))
	if err != nil {
		t.Fatalf("ChatDeleteRange: %v", err)
	}
	if !r.Known || r.Newer != 0 {
		t.Fatalf("range = %+v, want a known chat with nothing newer", r)
	}
	if r.Last == nil || r.Last.MsgID != "mine" || !r.Last.FromMe || !r.Last.Timestamp.Equal(through.Add(-time.Minute)) {
		t.Fatalf("last = %+v, want the newest message of his own", r.Last)
	}

	add("same-second", through, nil)
	r, err = db.ChatDeleteRange([]string{chat}, through.Add(500*time.Millisecond))
	if err != nil {
		t.Fatalf("ChatDeleteRange: %v", err)
	}
	if r.Last == nil || r.Last.MsgID != "same-second" || r.Newer != 0 {
		t.Fatalf("range = %+v last %+v, want the message in through's second as last", r, r.Last)
	}
}

func TestChatDeleteRangeCountsOnlyMessagesLaterThanThrough(t *testing.T) {
	db := openTestDB(t)
	chat := "15550002222@s.whatsapp.net"
	if err := db.UpsertChat(chat, "dm", "Elsa Moretti", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	through := time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)
	add := func(id string, at time.Time, apply func(*UpsertMessageParams)) {
		t.Helper()
		p := UpsertMessageParams{ChatJID: chat, MsgID: id, SenderJID: chat, Timestamp: at, Text: "text " + id}
		if apply != nil {
			apply(&p)
		}
		if err := db.UpsertMessage(p); err != nil {
			t.Fatalf("UpsertMessage %s: %v", id, err)
		}
	}
	add("seen", through.Add(-time.Minute), nil)
	// Neither a reaction nor a content-free system event is a message the
	// caller has not seen.
	add("reaction", through.Add(time.Minute), func(p *UpsertMessageParams) {
		p.ReactionToID = "seen"
		p.ReactionEmoji = "😂"
	})
	add("system", through.Add(2*time.Minute), func(p *UpsertMessageParams) { p.Text = ""; p.DisplayText = "(message)" })

	r, err := db.ChatDeleteRange([]string{chat}, through)
	if err != nil {
		t.Fatalf("ChatDeleteRange: %v", err)
	}
	if r.Newer != 0 || r.Last == nil || r.Last.MsgID != "seen" {
		t.Fatalf("range = %+v last %+v, want nothing newer and seen as last", r, r.Last)
	}

	// A message of his own from the phone counts like anybody's.
	add("from-phone", through.Add(time.Second), func(p *UpsertMessageParams) { p.FromMe = true; p.SenderJID = "" })
	add("theirs", through.Add(3*time.Minute), nil)
	r, err = db.ChatDeleteRange([]string{chat}, through)
	if err != nil {
		t.Fatalf("ChatDeleteRange: %v", err)
	}
	if r.Newer != 2 || !r.NewestAt.Equal(through.Add(3*time.Minute)) {
		t.Fatalf("range = %+v, want two newer messages, the newest at %s", r, through.Add(3*time.Minute))
	}
}

func TestChatDeleteRangeReadsEveryJIDOfTheChat(t *testing.T) {
	db := openTestDB(t)
	pn := "15550003333@s.whatsapp.net"
	lid := "200000000000001@lid"
	through := time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)
	for _, chat := range []string{pn, lid} {
		if err := db.UpsertChat(chat, "dm", "Pia Ferraro", time.Now()); err != nil {
			t.Fatalf("UpsertChat %s: %v", chat, err)
		}
	}
	if err := db.UpsertMessage(UpsertMessageParams{ChatJID: pn, MsgID: "pn-old", SenderJID: pn, Timestamp: through.Add(-2 * time.Hour), Text: "old"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMessage(UpsertMessageParams{ChatJID: lid, MsgID: "lid-new", SenderJID: lid, Timestamp: through.Add(-time.Hour), Text: "new"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMessage(UpsertMessageParams{ChatJID: lid, MsgID: "lid-later", SenderJID: lid, Timestamp: through.Add(time.Hour), Text: "later"}); err != nil {
		t.Fatal(err)
	}

	r, err := db.ChatDeleteRange([]string{pn, lid}, through)
	if err != nil {
		t.Fatalf("ChatDeleteRange: %v", err)
	}
	if !r.Known || r.Last == nil || r.Last.MsgID != "lid-new" || r.Last.ChatJID != lid || r.Newer != 1 {
		t.Fatalf("range = %+v last %+v, want lid-new as last and one newer across both JIDs", r, r.Last)
	}
	r, err = db.ChatDeleteRange([]string{pn}, through)
	if err != nil {
		t.Fatalf("ChatDeleteRange: %v", err)
	}
	if r.Last == nil || r.Last.MsgID != "pn-old" || r.Newer != 0 {
		t.Fatalf("range = %+v last %+v, want the phone number's rows alone", r, r.Last)
	}
}

func TestChatDeleteRangeKnowsAChatWithoutMessagesAndNoUnknownOne(t *testing.T) {
	db := openTestDB(t)
	chat := "15550004444@s.whatsapp.net"
	through := time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)

	r, err := db.ChatDeleteRange([]string{chat}, through)
	if err != nil {
		t.Fatalf("ChatDeleteRange: %v", err)
	}
	if r.Known || r.Last != nil || r.Newer != 0 {
		t.Fatalf("range = %+v, want an unknown chat", r)
	}

	if err := db.UpsertChat(chat, "dm", "Ivo Marchetti", time.Time{}); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	r, err = db.ChatDeleteRange([]string{chat}, through)
	if err != nil {
		t.Fatalf("ChatDeleteRange: %v", err)
	}
	if !r.Known || r.Last != nil || r.Newer != 0 {
		t.Fatalf("range = %+v, want a known chat without messages", r)
	}
}

func TestChatDeleteRangeRefusesNoJIDsAndNoTime(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.ChatDeleteRange(nil, time.Now()); err == nil {
		t.Fatal("no JIDs: want an error")
	}
	if _, err := db.ChatDeleteRange([]string{"15550005555@s.whatsapp.net"}, time.Time{}); err == nil {
		t.Fatal("no time: want an error")
	}
}

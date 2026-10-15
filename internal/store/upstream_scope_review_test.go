package store

import (
	"testing"
	"time"
)

// Edits can arrive through sync upserts as well as the explicit edit command.
// Older edits and the original sync echo must not replace the newest payload.
func TestUpstreamScopeExactEditedUpserts(t *testing.T) {
	for _, fromMe := range []bool{false, true} {
		db := openTestDB(t)
		chat := "15550000001@s.whatsapp.net"
		base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
		if err := db.UpsertChat(chat, "dm", "Fixture", base); err != nil {
			t.Fatal(err)
		}
		wantText, wantCaption := " \t newest\r\nbody α \n", "\n newest caption β \t"
		for _, row := range []UpsertMessageParams{
			{ChatJID: chat, MsgID: "edited", Timestamp: base, FromMe: fromMe, Text: "original", MediaCaption: "original"},
			{ChatJID: chat, MsgID: "edited", Timestamp: base.Add(2 * time.Minute), FromMe: fromMe, Edited: true, Text: wantText, MediaCaption: wantCaption},
			{ChatJID: chat, MsgID: "edited", Timestamp: base.Add(time.Minute), FromMe: fromMe, Edited: true, Text: "older", MediaCaption: "older"},
			{ChatJID: chat, MsgID: "edited", Timestamp: base, FromMe: fromMe, Text: "original", MediaCaption: "original"},
		} {
			if err := db.UpsertMessage(row); err != nil {
				t.Fatal(err)
			}
		}
		got, err := db.GetMessage(chat, "edited")
		if err != nil {
			t.Fatal(err)
		}
		if got.Text != wantText || got.MediaCaption != wantCaption || !got.Edited {
			t.Fatalf("newest exact edit lost (fromMe=%v): text=%q caption=%q edited=%v", fromMe, got.Text, got.MediaCaption, got.Edited)
		}
		listed, err := db.ListMessages(ListMessagesParams{ChatJID: chat, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(listed) != 1 || listed[0].Text != wantText || listed[0].MediaCaption != wantCaption {
			t.Fatalf("list changed exact payload: %+v", listed)
		}
		found, err := db.SearchMessages(SearchMessagesParams{ChatJID: chat, Query: "newest", Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(found) != 1 || found[0].Text != wantText || found[0].MediaCaption != wantCaption {
			t.Fatalf("search changed exact payload: %+v", found)
		}
	}
}

// Exercise the production store path with exact payloads, including values
// whose surrounding whitespace has meaning. Display previews are separate.
func TestUpstreamScopeExactHistoryContent(t *testing.T) {
	for _, fromMe := range []bool{false, true} {
		name := "incoming"
		if fromMe {
			name = "outgoing"
		}
		t.Run(name, func(t *testing.T) {
			db := openTestDB(t)
			chat := "15550000001@s.whatsapp.net"
			now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
			if err := db.UpsertChat(chat, "dm", "Fixture", now); err != nil {
				t.Fatal(err)
			}
			for _, sample := range []struct{ id, text, caption string }{
				{"multiline", "  first\r\nsecond \t\n", "\n caption α \n"},
				{"whitespace", " \t\n", "\r\n  "},
				{"empty", "", ""},
			} {
				// Repeated storage must preserve the same exact bytes too.
				for repeat := 0; repeat < 2; repeat++ {
					if err := db.UpsertMessage(UpsertMessageParams{ChatJID: chat, MsgID: sample.id, Timestamp: now, FromMe: fromMe, Text: sample.text, MediaCaption: sample.caption}); err != nil {
						t.Fatal(err)
					}
					got, err := db.GetMessage(chat, sample.id)
					if err != nil {
						t.Fatal(err)
					}
					if got.Text != sample.text || got.MediaCaption != sample.caption {
						t.Errorf("%s repeat %d: want text=%q caption=%q; got text=%q caption=%q", sample.id, repeat, sample.text, sample.caption, got.Text, got.MediaCaption)
					}
				}
			}
		})
	}
}

// Editing must not undo exact preservation from the initial insert, and must
// retain the independently stored identity of the message being quoted.
func TestUpstreamScopeEditedHistoryContent(t *testing.T) {
	db := openTestDB(t)
	chat := "15550000001@s.whatsapp.net"
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if err := db.UpsertChat(chat, "dm", "Fixture", now); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMessage(UpsertMessageParams{ChatJID: chat, MsgID: "reply", Timestamp: now, Text: "original", QuotedMsgID: "quoted", QuotedSenderJID: chat}); err != nil {
		t.Fatal(err)
	}
	// Include whitespace-only and empty replacements: normalization must not
	// collapse meaningful whitespace or change the existing empty convention.
	for _, want := range []string{" \t edited\r\nsecond line \n", " \t\n", ""} {
		if err := db.UpdateMessageText(chat, "reply", want); err != nil {
			t.Fatal(err)
		}
		got, err := db.GetMessage(chat, "reply")
		if err != nil {
			t.Fatal(err)
		}
		if got.Text != want {
			t.Errorf("edit changed payload: want=%q got=%q", want, got.Text)
		}
		if got.QuotedMsgID != "quoted" || got.QuotedSenderJID != chat {
			t.Errorf("edit changed quoted identity: id=%q sender=%q", got.QuotedMsgID, got.QuotedSenderJID)
		}
	}
}

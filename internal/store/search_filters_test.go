package store

import (
	"strings"
	"testing"
	"time"
)

func TestSearchMessagesFiltersByMediaAndType(t *testing.T) {
	db := openTestDB(t)

	chat := "123@s.whatsapp.net"
	if err := db.UpsertChat(chat, "dm", "Alice", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}

	base := time.Date(2024, 4, 1, 0, 0, 0, 0, time.UTC)
	rows := []UpsertMessageParams{
		{
			ChatJID:    chat,
			ChatName:   "Alice",
			MsgID:      "text-1",
			SenderJID:  chat,
			SenderName: "Alice",
			Timestamp:  base,
			Text:       "quarterly report ready",
		},
		{
			ChatJID:      chat,
			ChatName:     "Alice",
			MsgID:        "image-1",
			SenderJID:    chat,
			SenderName:   "Alice",
			Timestamp:    base.Add(time.Second),
			MediaType:    "image",
			MediaCaption: "quarterly report screenshot",
			Filename:     "report.png",
			MimeType:     "image/png",
		},
		{
			ChatJID:      chat,
			ChatName:     "Alice",
			MsgID:        "document-1",
			SenderJID:    chat,
			SenderName:   "Alice",
			Timestamp:    base.Add(2 * time.Second),
			MediaType:    "document",
			MediaCaption: "quarterly report attachment",
			Filename:     "report.pdf",
			MimeType:     "application/pdf",
		},
		{
			ChatJID:         chat,
			ChatName:        "Alice",
			MsgID:           "forwarded-1",
			SenderJID:       chat,
			SenderName:      "Alice",
			Timestamp:       base.Add(3 * time.Second),
			Text:            "forwarded memo",
			IsForwarded:     true,
			ForwardingScore: 1,
		},
	}
	for _, row := range rows {
		if err := db.UpsertMessage(row); err != nil {
			t.Fatalf("UpsertMessage %s: %v", row.MsgID, err)
		}
	}
	if err := db.SetStarred(SetStarredParams{
		ChatJID:   chat,
		MsgID:     "image-1",
		SenderJID: chat,
		Starred:   true,
		StarredAt: base.Add(4 * time.Second),
	}); err != nil {
		t.Fatalf("SetStarred: %v", err)
	}

	tests := []struct {
		name string
		p    SearchMessagesParams
		want string
	}{
		{
			name: "all matches",
			p:    SearchMessagesParams{Query: "report", Limit: 10},
			want: "document-1,image-1,text-1",
		},
		{
			name: "has media",
			p:    SearchMessagesParams{Query: "report", Limit: 10, HasMedia: true},
			want: "document-1,image-1",
		},
		{
			name: "text type",
			p:    SearchMessagesParams{Query: "report", Limit: 10, Type: " text "},
			want: "text-1",
		},
		{
			name: "image type case insensitive",
			p:    SearchMessagesParams{Query: "report", Limit: 10, Type: "IMAGE"},
			want: "image-1",
		},
		{
			name: "has media plus concrete media type",
			p:    SearchMessagesParams{Query: "report", Limit: 10, HasMedia: true, Type: "document"},
			want: "document-1",
		},
		{
			name: "forwarded",
			p:    SearchMessagesParams{Query: "forwarded", Limit: 10, Forwarded: true},
			want: "forwarded-1",
		},
		{
			name: "starred",
			p:    SearchMessagesParams{Query: "report", Limit: 10, Starred: true},
			want: "image-1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := db.SearchMessages(tc.p)
			if err != nil {
				t.Fatalf("SearchMessages: %v", err)
			}
			if ids := messageIDs(got); ids != tc.want {
				t.Fatalf("ids = %q, want %q", ids, tc.want)
			}
			if tc.name == "all matches" && got[2].SenderName != "Alice" {
				t.Fatalf("SenderName = %q, want Alice", got[2].SenderName)
			}
		})
	}
}

func TestSearchMessagesFiltersMultipleChatJIDs(t *testing.T) {
	db := openTestDB(t)
	pn := "15551234567@s.whatsapp.net"
	lid := "123456789@lid"
	other := "other@s.whatsapp.net"
	base := time.Date(2024, 4, 1, 0, 0, 0, 0, time.UTC)
	for _, jid := range []string{pn, lid, other} {
		if err := db.UpsertChat(jid, "dm", jid, base); err != nil {
			t.Fatalf("UpsertChat %s: %v", jid, err)
		}
	}
	rows := []UpsertMessageParams{
		{ChatJID: pn, MsgID: "pn-row", SenderJID: pn, Timestamp: base, Text: "shared needle phone"},
		{ChatJID: lid, MsgID: "lid-row", SenderJID: lid, Timestamp: base.Add(time.Second), Text: "shared needle hidden"},
		{ChatJID: other, MsgID: "other-row", SenderJID: other, Timestamp: base.Add(2 * time.Second), Text: "shared needle other"},
	}
	for _, row := range rows {
		if err := db.UpsertMessage(row); err != nil {
			t.Fatalf("UpsertMessage %s: %v", row.MsgID, err)
		}
	}

	msgs, err := db.SearchMessages(SearchMessagesParams{Query: "needle", ChatJIDs: []string{pn, lid}, Limit: 10})
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if got := messageIDs(msgs); got != "lid-row,pn-row" {
		t.Fatalf("ids = %s", got)
	}
}

func TestSearchMessagesRejectsInvalidMediaFilters(t *testing.T) {
	db := openTestDB(t)

	tests := []struct {
		name    string
		p       SearchMessagesParams
		wantErr string
	}{
		{
			name:    "contradictory text and has media",
			p:       SearchMessagesParams{Query: "report", HasMedia: true, Type: "text"},
			wantErr: "cannot combine",
		},
		{
			name:    "unsupported type",
			p:       SearchMessagesParams{Query: "report", Type: "sticker"},
			wantErr: "unsupported message type",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := db.SearchMessages(tc.p)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

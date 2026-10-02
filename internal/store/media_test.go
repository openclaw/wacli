package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPendingMediaQueriesHonorCanceledContext(t *testing.T) {
	db := openTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := db.CountPendingMediaDownloads(ctx, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("CountPendingMediaDownloads error = %v, want context.Canceled", err)
	}
	if _, err := db.ListPendingMediaDownloads(ctx, "", "", 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListPendingMediaDownloads error = %v, want context.Canceled", err)
	}
	if _, err := db.ListPendingMediaBefore(ctx, "", "", 1, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListPendingMediaBefore error = %v, want context.Canceled", err)
	}
	if err := db.MarkMediaUnavailable(ctx, "chat", "msg", nowUTC()); !errors.Is(err, context.Canceled) {
		t.Fatalf("MarkMediaUnavailable error = %v, want context.Canceled", err)
	}
}

func TestPendingMediaQueriesFilterByMediaType(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	chat := "123@s.whatsapp.net"
	if err := db.UpsertChat(chat, "dm", "Chat", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	base := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	// The image is the newest row, so a limit applied before the type filter
	// would return it instead of the newest voice note.
	for i, m := range []struct{ id, mediaType string }{
		{"voice-old", "audio"},
		{"voice-new", "audio"},
		{"photo", "image"},
	} {
		if err := db.UpsertMessage(UpsertMessageParams{
			ChatJID:    chat,
			MsgID:      m.id,
			Timestamp:  base.Add(time.Duration(i) * time.Hour),
			MediaType:  m.mediaType,
			DirectPath: "/direct/" + m.id,
			MediaKey:   []byte{1},
		}); err != nil {
			t.Fatalf("UpsertMessage %s: %v", m.id, err)
		}
	}
	ids := func(rows []PendingMediaDownload) []string {
		out := make([]string, 0, len(rows))
		for _, row := range rows {
			out = append(out, row.MsgID)
		}
		return out
	}
	for _, tc := range []struct {
		name string
		list func() ([]PendingMediaDownload, error)
		want []string
	}{
		{"all types", func() ([]PendingMediaDownload, error) { return db.ListPendingMediaDownloads(ctx, "", "", 0) }, []string{"photo", "voice-new", "voice-old"}},
		{"audio", func() ([]PendingMediaDownload, error) { return db.ListPendingMediaDownloads(ctx, "", "audio", 0) }, []string{"voice-new", "voice-old"}},
		{"audio limit", func() ([]PendingMediaDownload, error) { return db.ListPendingMediaDownloads(ctx, "", "audio", 1) }, []string{"voice-new"}},
		{"audio in chat", func() ([]PendingMediaDownload, error) { return db.ListPendingMediaDownloads(ctx, chat, "audio", 0) }, []string{"voice-new", "voice-old"}},
		{"no match", func() ([]PendingMediaDownload, error) { return db.ListPendingMediaDownloads(ctx, "", "video", 0) }, []string{}},
		{"audio before", func() ([]PendingMediaDownload, error) {
			return db.ListPendingMediaBefore(ctx, "", "audio", base.Add(90*time.Minute).Unix(), 0)
		}, []string{"voice-new", "voice-old"}},
		{"image before", func() ([]PendingMediaDownload, error) {
			return db.ListPendingMediaBefore(ctx, "", "image", base.Add(90*time.Minute).Unix(), 0)
		}, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := tc.list()
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if got := ids(rows); strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("pending = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFreshMediaMetadataClearsUnavailableState(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	chat := "123@s.whatsapp.net"
	if err := db.UpsertChat(chat, "dm", "Chat", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	message := UpsertMessageParams{
		ChatJID:    chat,
		MsgID:      "m1",
		Timestamp:  time.Now(),
		MediaType:  "image",
		DirectPath: "/old",
		MediaKey:   []byte{1},
	}
	if err := db.UpsertMessage(message); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}
	if err := db.MarkMediaUnavailable(ctx, chat, "m1", time.Now()); err != nil {
		t.Fatalf("MarkMediaUnavailable: %v", err)
	}

	message.DirectPath = "/fresh"
	message.Timestamp = message.Timestamp.Add(time.Second)
	if err := db.UpsertMessage(message); err != nil {
		t.Fatalf("UpsertMessage fresh path: %v", err)
	}
	assertMediaUnavailableCleared(t, db, chat, "m1")

	if err := db.MarkMediaUnavailable(ctx, chat, "m1", time.Now()); err != nil {
		t.Fatalf("MarkMediaUnavailable again: %v", err)
	}
	message.DirectPath = "/stale"
	message.Timestamp = message.Timestamp.Add(-time.Minute)
	if err := db.UpsertMessage(message); err != nil {
		t.Fatalf("UpsertMessage stale path: %v", err)
	}
	var directPath string
	var unavailableAt sql.NullInt64
	if err := db.sql.QueryRow(`SELECT direct_path, media_unavailable_at FROM messages WHERE chat_jid = ? AND msg_id = ?`, chat, "m1").Scan(&directPath, &unavailableAt); err != nil {
		t.Fatalf("query stale upsert result: %v", err)
	}
	if directPath != "/fresh" {
		t.Fatalf("direct_path = %q, want /fresh", directPath)
	}
	if !unavailableAt.Valid {
		t.Fatal("stale media metadata cleared media_unavailable_at")
	}
	if err := db.MarkMediaDownloaded(chat, "m1", "/tmp/m1", time.Now()); err != nil {
		t.Fatalf("MarkMediaDownloaded: %v", err)
	}
	assertMediaUnavailableCleared(t, db, chat, "m1")
}

func assertMediaUnavailableCleared(t *testing.T, db *DB, chatJID, msgID string) {
	t.Helper()
	var unavailableAt sql.NullInt64
	if err := db.sql.QueryRow(`SELECT media_unavailable_at FROM messages WHERE chat_jid = ? AND msg_id = ?`, chatJID, msgID).Scan(&unavailableAt); err != nil {
		t.Fatalf("query media_unavailable_at: %v", err)
	}
	if unavailableAt.Valid {
		t.Fatalf("media_unavailable_at still set: %d", unavailableAt.Int64)
	}
}

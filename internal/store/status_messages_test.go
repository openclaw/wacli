package store

import (
	"testing"
	"time"
)

func TestStatusMessagesStoredSeparatelyFromMessages(t *testing.T) {
	db := openTestDB(t)
	ts := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)

	if err := db.UpsertStatusMessage(UpsertStatusMessageParams{
		MsgID:           "status-1",
		Timestamp:       ts,
		FromMe:          true,
		Text:            "test",
		BackgroundColor: "#ff00aa",
		Font:            2,
	}); err != nil {
		t.Fatalf("UpsertStatusMessage: %v", err)
	}
	if err := db.UpsertStatusMessage(UpsertStatusMessageParams{
		MsgID:     "status-1",
		Timestamp: ts.Add(time.Minute),
		FromMe:    true,
		Text:      "test edited locally",
	}); err != nil {
		t.Fatalf("UpsertStatusMessage duplicate: %v", err)
	}

	if got := countRows(t, db.sql, "SELECT COUNT(*) FROM messages WHERE msg_id = ?", "status-1"); got != 0 {
		t.Fatalf("expected no regular message rows, got %d", got)
	}
	if got := countRows(t, db.sql, "SELECT COUNT(*) FROM status_messages WHERE msg_id = ?", "status-1"); got != 1 {
		t.Fatalf("expected one status message row, got %d", got)
	}
	status, err := db.GetStatusMessage("status-1")
	if err != nil {
		t.Fatalf("GetStatusMessage: %v", err)
	}
	if status.MsgID != "status-1" || status.Text != "test edited locally" || !status.FromMe {
		t.Fatalf("unexpected status message: %+v", status)
	}
}

func TestStatusMessageStoresMediaMetadata(t *testing.T) {
	db := openTestDB(t)
	ts := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)
	if err := db.UpsertStatusMessage(UpsertStatusMessageParams{
		MsgID:         "status-media",
		Timestamp:     ts,
		FromMe:        true,
		Text:          "caption",
		MediaType:     "image",
		MediaCaption:  "caption",
		Filename:      "photo.jpg",
		MimeType:      "image/jpeg",
		DirectPath:    "/media/path",
		MediaKey:      []byte("key"),
		FileSHA256:    []byte("sha"),
		FileEncSHA256: []byte("enc"),
		FileLength:    123,
	}); err != nil {
		t.Fatalf("UpsertStatusMessage: %v", err)
	}
	status, err := db.GetStatusMessage("status-media")
	if err != nil {
		t.Fatalf("GetStatusMessage: %v", err)
	}
	if status.MediaType != "image" || status.Filename != "photo.jpg" || status.MimeType != "image/jpeg" || status.DirectPath != "/media/path" || status.FileLength != 123 {
		t.Fatalf("unexpected media status metadata: %+v", status)
	}
}

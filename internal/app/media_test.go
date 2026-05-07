package app

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
)

func TestDownloadMediaJobMarksDownloaded(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := "123@s.whatsapp.net"
	if err := a.db.UpsertChat(chat, "dm", "Alice", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := a.db.UpsertMessage(store.UpsertMessageParams{
		ChatJID:       chat,
		MsgID:         "mid",
		SenderJID:     chat,
		SenderName:    "Alice",
		Timestamp:     time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC),
		FromMe:        false,
		Text:          "",
		MediaType:     "image",
		MediaCaption:  "cap",
		Filename:      "pic.jpg",
		MimeType:      "image/jpeg",
		DirectPath:    "/direct/path",
		MediaKey:      []byte{1, 2, 3},
		FileSHA256:    []byte{4, 5},
		FileEncSHA256: []byte{6, 7},
		FileLength:    123,
	}); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}

	if err := a.downloadMediaJob(context.Background(), mediaJob{chatJID: chat, msgID: "mid"}); err != nil {
		t.Fatalf("downloadMediaJob: %v", err)
	}

	info, err := a.db.GetMediaDownloadInfo(chat, "mid")
	if err != nil {
		t.Fatalf("GetMediaDownloadInfo: %v", err)
	}
	if info.LocalPath == "" {
		t.Fatalf("expected LocalPath to be set")
	}
	if _, err := os.Stat(info.LocalPath); err != nil {
		t.Fatalf("expected downloaded file to exist: %v", err)
	}
}

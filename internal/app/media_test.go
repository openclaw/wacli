package app

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
)

func TestMediaQueueWaitIdleFencesActiveProducer(t *testing.T) {
	queue := newMediaQueue(1)
	if !queue.beginProducer() {
		t.Fatal("expected producer admission before drain")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	drained := make(chan bool, 1)
	go func() {
		drained <- queue.waitIdle(ctx)
	}()

	select {
	case <-drained:
		t.Fatal("queue reported idle while an event producer was active")
	case <-time.After(50 * time.Millisecond):
	}

	queue.endProducer()
	select {
	case ok := <-drained:
		if !ok {
			t.Fatal("queue drain was canceled")
		}
	case <-time.After(time.Second):
		t.Fatal("queue did not drain after producer finished")
	}
	if queue.beginProducer() {
		t.Fatal("queue admitted a producer after the drain fence")
	}
}

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

package app

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
)

func insertMediaMessage(t *testing.T, a *App, chat, id string, ts time.Time) {
	t.Helper()
	if err := a.db.UpsertMessage(store.UpsertMessageParams{
		ChatJID:       chat,
		MsgID:         id,
		SenderJID:     chat,
		SenderName:    "Alice",
		Timestamp:     ts,
		MediaType:     "image",
		Filename:      id + ".jpg",
		MimeType:      "image/jpeg",
		DirectPath:    "/direct/" + id,
		MediaKey:      []byte{1, 2, 3},
		FileSHA256:    []byte{4, 5},
		FileEncSHA256: []byte{6, 7},
		FileLength:    123,
	}); err != nil {
		t.Fatalf("UpsertMessage %s: %v", id, err)
	}
}

func TestBackfillMediaDownloadsPending(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()

	chat := "123@s.whatsapp.net"
	if err := a.db.UpsertChat(chat, "dm", "Alice", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	base := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	insertMediaMessage(t, a, chat, "m1", base)
	insertMediaMessage(t, a, chat, "m2", base.Add(time.Second))
	insertMediaMessage(t, a, chat, "m3", base.Add(2*time.Second))

	// A text-only message must be ignored.
	if err := a.db.UpsertMessage(store.UpsertMessageParams{
		ChatJID: chat, MsgID: "text", SenderJID: chat, Timestamp: base, Text: "hi",
	}); err != nil {
		t.Fatalf("UpsertMessage text: %v", err)
	}

	res, err := a.BackfillMedia(context.Background(), BackfillMediaOptions{})
	if err != nil {
		t.Fatalf("BackfillMedia: %v", err)
	}
	if res.Pending != 3 || res.Attempted != 3 || res.Downloaded != 3 || res.Failed != 0 || res.Skipped != 0 {
		t.Fatalf("unexpected result: %+v", res)
	}

	for _, id := range []string{"m1", "m2", "m3"} {
		info, err := a.db.GetMediaDownloadInfo(chat, id)
		if err != nil {
			t.Fatalf("GetMediaDownloadInfo %s: %v", id, err)
		}
		if info.LocalPath == "" {
			t.Fatalf("expected LocalPath set for %s", id)
		}
		if _, err := os.Stat(info.LocalPath); err != nil {
			t.Fatalf("expected file for %s: %v", id, err)
		}
	}

	// A second run has nothing left to do.
	res2, err := a.BackfillMedia(context.Background(), BackfillMediaOptions{})
	if err != nil {
		t.Fatalf("BackfillMedia rerun: %v", err)
	}
	if res2.Pending != 0 || res2.Attempted != 0 || res2.Downloaded != 0 {
		t.Fatalf("expected nothing pending on rerun, got %+v", res2)
	}
}

func TestBackfillMediaLimitAndChatFilter(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()

	chatA := "111@s.whatsapp.net"
	chatB := "222@s.whatsapp.net"
	for _, c := range []string{chatA, chatB} {
		if err := a.db.UpsertChat(c, "dm", "x", time.Now()); err != nil {
			t.Fatalf("UpsertChat %s: %v", c, err)
		}
	}
	base := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	insertMediaMessage(t, a, chatA, "a1", base)
	insertMediaMessage(t, a, chatA, "a2", base)
	insertMediaMessage(t, a, chatB, "b1", base) // newest row among equal timestamps

	// Limit caps attempts but reports the full pending total. Newest-first,
	// rowid-stable ordering means the single attempt is b1.
	res, err := a.BackfillMedia(context.Background(), BackfillMediaOptions{Limit: 1})
	if err != nil {
		t.Fatalf("BackfillMedia: %v", err)
	}
	if res.Pending != 3 || res.Attempted != 1 || res.Downloaded != 1 {
		t.Fatalf("limit result unexpected: %+v", res)
	}
	if n, err := a.db.CountPendingMediaDownloads(context.Background(), chatB); err != nil || n != 0 {
		t.Fatalf("expected chatB drained by limit run, got %d (err %v)", n, err)
	}

	// Chat filter scopes the remaining work to one chat only.
	res, err = a.BackfillMedia(context.Background(), BackfillMediaOptions{ChatJID: chatA})
	if err != nil {
		t.Fatalf("BackfillMedia chatA: %v", err)
	}
	if res.Pending != 2 || res.Attempted != 2 || res.Downloaded != 2 {
		t.Fatalf("chat filter result unexpected: %+v", res)
	}
	// Everything is now downloaded.
	if n, err := a.db.CountPendingMediaDownloads(context.Background(), ""); err != nil || n != 0 {
		t.Fatalf("expected 0 pending overall, got %d (err %v)", n, err)
	}
}

func TestBackfillMediaCanceledBeforeDispatch(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()

	chat := "123@s.whatsapp.net"
	if err := a.db.UpsertChat(chat, "dm", "Alice", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	insertMediaMessage(t, a, chat, "m1", time.Now())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := a.BackfillMedia(ctx, BackfillMediaOptions{})
	if err != context.Canceled {
		t.Fatalf("BackfillMedia error = %v, want context.Canceled", err)
	}
	if res != (BackfillMediaResult{}) {
		t.Fatalf("unexpected canceled result: %+v", res)
	}
}

func TestBackfillMediaRejectsNegativeOptions(t *testing.T) {
	a := newTestApp(t)

	if _, err := a.BackfillMedia(context.Background(), BackfillMediaOptions{Limit: -1}); err == nil {
		t.Fatalf("expected negative limit error")
	}
	if _, err := a.BackfillMedia(context.Background(), BackfillMediaOptions{Workers: -1}); err == nil {
		t.Fatalf("expected negative workers error")
	}
}

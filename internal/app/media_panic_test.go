package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
)

// panicFirstWA wraps a fakeWA but panics on the first DownloadMediaToFile
// call, mirroring the behavior of an unexpected media payload that
// dereferences a nil field inside whatsmeow.
type panicFirstWA struct {
	*fakeWA
	panics  atomic.Int32
	allowed atomic.Int32
}

func (p *panicFirstWA) DownloadMediaToFile(ctx context.Context, directPath string, encFileHash, fileHash, mediaKey []byte, fileLength uint64, mediaType, mmsType string, targetPath string) (int64, error) {
	if p.panics.Add(1) == 1 {
		panic("simulated bad media payload")
	}
	p.allowed.Add(1)
	return p.fakeWA.DownloadMediaToFile(ctx, directPath, encFileHash, fileHash, mediaKey, fileLength, mediaType, mmsType, targetPath)
}

// TestMediaWorkerSurvivesPanic verifies that a panic in one media job does
// not take down the worker goroutine: subsequent jobs enqueued on the same
// channel must still be processed (#176, regression of #143).
func TestMediaWorkerSurvivesPanic(t *testing.T) {
	a := newTestApp(t)
	pf := &panicFirstWA{fakeWA: newFakeWA()}
	a.wa = pf

	chat := "123@s.whatsapp.net"
	if err := a.db.UpsertChat(chat, "dm", "Alice", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	for _, m := range []struct{ id string }{{"boom"}, {"ok"}} {
		if err := a.db.UpsertMessage(store.UpsertMessageParams{
			ChatJID:       chat,
			MsgID:         m.id,
			SenderJID:     chat,
			SenderName:    "Alice",
			Timestamp:     time.Now(),
			MediaType:     "image",
			Filename:      m.id + ".jpg",
			MimeType:      "image/jpeg",
			DirectPath:    "/direct/path",
			MediaKey:      []byte{1, 2, 3},
			FileSHA256:    []byte{4, 5},
			FileEncSHA256: []byte{6, 7},
			FileLength:    16,
		}); err != nil {
			t.Fatalf("UpsertMessage %s: %v", m.id, err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	queue := newMediaQueue(2)
	wait, cancelWorkers, err := a.runMediaWorkers(ctx, queue, 1)
	if err != nil {
		t.Fatalf("runMediaWorkers: %v", err)
	}
	defer func() {
		cancelWorkers()
		wait()
	}()

	queue.enqueue(ctx, mediaJob{chatJID: chat, msgID: "boom"})
	queue.enqueue(ctx, mediaJob{chatJID: chat, msgID: "ok"})

	// Poll until the benign job is persisted. DownloadMediaToFile returning only
	// proves the worker reached the second job; MarkMediaDownloaded can still be
	// a few scheduler ticks behind on a busy test runner.
	deadline := time.Now().Add(2 * time.Second)
	var localPath string
	for time.Now().Before(deadline) {
		info, err := a.db.GetMediaDownloadInfo(chat, "ok")
		if err == nil && info.LocalPath != "" {
			localPath = info.LocalPath
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if pf.panics.Load() != 2 {
		// Add returns the new value, so 2 here means both jobs reached the
		// download call (the first panicked, the second did not).
		t.Fatalf("expected both jobs to reach the download call, got panics=%d", pf.panics.Load())
	}
	if pf.allowed.Load() != 1 {
		t.Fatalf("benign job was not processed after panic: allowed=%d", pf.allowed.Load())
	}

	if localPath == "" {
		t.Fatalf("expected LocalPath for benign job to be set after panic survival")
	}

}

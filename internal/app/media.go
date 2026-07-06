package app

import (
	"context"
	"database/sql"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"

	"github.com/openclaw/wacli/internal/fsutil"
	"github.com/openclaw/wacli/internal/pathutil"
	"github.com/openclaw/wacli/internal/store"
)

type mediaJob struct {
	chatJID string
	msgID   string
}

type mediaQueue struct {
	jobs      chan mediaJob
	mu        sync.Mutex
	pending   int
	producers int
	accepting bool
	changed   chan struct{}
}

func newMediaQueue(buffer int) *mediaQueue {
	return &mediaQueue{
		jobs:      make(chan mediaJob, buffer),
		accepting: true,
		changed:   make(chan struct{}, 1),
	}
}

func (q *mediaQueue) beginProducer() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.accepting {
		return false
	}
	q.producers++
	q.notify()
	return true
}

func (q *mediaQueue) endProducer() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.producers--
	if q.producers < 0 {
		panic("media queue producer count became negative")
	}
	q.notify()
}

func (q *mediaQueue) enqueue(ctx context.Context, job mediaJob) {
	q.mu.Lock()
	q.pending++
	q.mu.Unlock()
	q.notify()
	select {
	case q.jobs <- job:
	case <-ctx.Done():
		q.done()
	}
}

func (q *mediaQueue) done() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.pending--
	if q.pending < 0 {
		panic("media queue pending count became negative")
	}
	q.notify()
}

func (q *mediaQueue) notify() {
	select {
	case q.changed <- struct{}{}:
	default:
	}
}

// waitIdle keeps media intake open while waiting, then atomically fences new
// event producers once no callback or media job remains. This avoids a long
// intake blackout during slow downloads without racing an in-flight handler.
func (q *mediaQueue) waitIdle(ctx context.Context) bool {
	for {
		q.mu.Lock()
		idle := q.pending == 0 && q.producers == 0
		if idle {
			q.accepting = false
		}
		q.mu.Unlock()
		if idle {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-q.changed:
		}
	}
}

func (a *App) ResolveMediaOutputPath(info store.MediaDownloadInfo, requested string) (string, error) {
	filename := mediaFilename(info)

	if strings.TrimSpace(requested) != "" {
		out := requested
		if !filepath.IsAbs(out) {
			if abs, err := filepath.Abs(out); err == nil {
				out = abs
			}
		}
		if st, err := os.Stat(out); err == nil && st.IsDir() {
			return filepath.Join(out, filename), nil
		}
		if strings.HasSuffix(out, string(os.PathSeparator)) {
			return filepath.Join(out, filename), nil
		}
		return out, nil
	}

	baseDir := filepath.Join(a.opts.StoreDir, "media", pathutil.SanitizeSegment(info.ChatJID), pathutil.SanitizeSegment(info.MsgID))
	if info.MediaType != "" {
		baseDir = filepath.Join(baseDir, pathutil.SanitizeSegment(info.MediaType))
	}
	if abs, err := filepath.Abs(baseDir); err == nil {
		baseDir = abs
	}
	return filepath.Join(baseDir, filename), nil
}

func mediaFilename(info store.MediaDownloadInfo) string {
	name := strings.TrimSpace(info.Filename)
	ext := ""
	if strings.TrimSpace(info.MimeType) != "" {
		if exts, err := mime.ExtensionsByType(info.MimeType); err == nil && len(exts) > 0 {
			ext = exts[0]
		}
	}

	if name == "" {
		base := "message-" + pathutil.SanitizeSegment(info.MsgID)
		if ext == "" {
			ext = ".bin"
		}
		return pathutil.SanitizeFilename(base + ext)
	}

	name = pathutil.SanitizeFilename(name)
	if ext != "" && filepath.Ext(name) == "" {
		name += ext
	}
	return name
}

func (a *App) runMediaWorkers(ctx context.Context, queue *mediaQueue, workers int) (wait func(), cancel func(), err error) {
	if workers <= 0 {
		workers = 2
	}
	if queue == nil {
		return func() {}, func() {}, nil
	}

	ctx, cancelWorkers := context.WithCancel(ctx)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case job := <-queue.jobs:
					// Recover per job so a panic fails one download
					// instead of killing the worker permanently (#52).
					func() {
						defer queue.done()
						defer func() {
							if r := recover(); r != nil {
								if a.eventsEnabled() {
									a.emitEvent("media_worker_panic", map[string]any{
										"chat_jid": job.chatJID,
										"msg_id":   job.msgID,
										"panic":    fmt.Sprint(r),
										"stack":    string(debug.Stack()),
									})
								} else {
									fmt.Fprintf(os.Stderr, "media worker panic (recovered) for %s/%s: %v\n%s\n",
										job.chatJID, job.msgID, r, debug.Stack())
								}
							}
						}()
						if strings.TrimSpace(job.chatJID) == "" || strings.TrimSpace(job.msgID) == "" {
							return
						}
						if err := a.downloadMediaJob(ctx, job); err != nil {
							a.emitWarning(
								"media_download_failed",
								fmt.Sprintf("media download failed for %s/%s: %v", job.chatJID, job.msgID, err),
								map[string]any{"chat_jid": job.chatJID, "msg_id": job.msgID, "error": err.Error()},
							)
						}
					}()
				}
			}
		}()
	}

	return wg.Wait, cancelWorkers, nil
}

func (a *App) downloadMediaJob(ctx context.Context, job mediaJob) error {
	info, err := a.db.GetMediaDownloadInfo(job.chatJID, job.msgID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	}
	if strings.TrimSpace(info.LocalPath) != "" {
		return nil
	}
	if strings.TrimSpace(info.MediaType) == "" || strings.TrimSpace(info.DirectPath) == "" || len(info.MediaKey) == 0 {
		return nil
	}

	targetPath, err := a.ResolveMediaOutputPath(info, "")
	if err != nil {
		return err
	}
	if err := fsutil.EnsurePrivateDir(filepath.Dir(targetPath)); err != nil {
		return err
	}

	if _, err := a.wa.DownloadMediaToFile(ctx, info.DirectPath, info.FileEncSHA256, info.FileSHA256, info.MediaKey, info.FileLength, info.MediaType, "", targetPath); err != nil {
		return err
	}

	now := nowUTC()
	return a.db.MarkMediaDownloaded(info.ChatJID, info.MsgID, targetPath, now)
}

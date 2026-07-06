package app

import (
	"context"
	"fmt"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/openclaw/wacli/internal/store"
)

// BackfillMediaOptions controls a one-shot backfill of media already recorded in
// the local database but not yet downloaded to disk.
type BackfillMediaOptions struct {
	// ChatJID, when non-empty, scopes the backfill to a single chat.
	ChatJID string
	// Limit caps how many pending downloads to attempt (<= 0 means all).
	Limit int
	// Workers is the number of concurrent downloads (defaults to 4).
	Workers int
}

// BackfillMediaResult summarises a backfill run.
type BackfillMediaResult struct {
	// Pending is the total number of downloadable-but-not-downloaded messages
	// matching the filter, before Limit is applied.
	Pending int
	// Attempted is how many downloads were tried (bounded by Limit).
	Attempted int
	// Downloaded is how many files were successfully fetched.
	Downloaded int
	// Skipped is how many jobs were already downloaded or lacked metadata by the
	// time the worker reached them.
	Skipped int
	// Failed is how many downloads returned an error.
	Failed int
}

// BackfillMedia downloads media for messages already stored in the local
// database that have downloadable metadata but no local copy yet. Unlike
// `sync --download-media`, which only fetches media for messages arriving during
// the sync session, this scans existing rows and fetches them over a single
// connection. The caller must have an authenticated, connected client.
func (a *App) BackfillMedia(ctx context.Context, opts BackfillMediaOptions) (BackfillMediaResult, error) {
	if err := ctx.Err(); err != nil {
		return BackfillMediaResult{}, err
	}
	if opts.Limit < 0 {
		return BackfillMediaResult{}, fmt.Errorf("limit must be >= 0")
	}
	if opts.Workers < 0 {
		return BackfillMediaResult{}, fmt.Errorf("workers must be >= 0")
	}
	opts.ChatJID = strings.TrimSpace(opts.ChatJID)

	workers := opts.Workers
	if workers <= 0 {
		workers = 4
	}

	pendingTotal, err := a.db.CountPendingMediaDownloads(ctx, opts.ChatJID)
	if err != nil {
		return BackfillMediaResult{}, fmt.Errorf("count pending media: %w", err)
	}
	result := BackfillMediaResult{Pending: pendingTotal}
	if pendingTotal == 0 {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		return result, nil
	}

	jobs, err := a.db.ListPendingMediaDownloads(ctx, opts.ChatJID, opts.Limit)
	if err != nil {
		return result, fmt.Errorf("list pending media: %w", err)
	}
	if len(jobs) == 0 {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		return result, nil
	}
	if workers > len(jobs) {
		workers = len(jobs)
	}

	a.emitEvent("media_backfill_start", map[string]any{
		"pending":  pendingTotal,
		"selected": len(jobs),
		"chat_jid": opts.ChatJID,
	})

	var attempted, downloaded, skipped, failed atomic.Int64
	jobCh := make(chan store.PendingMediaDownload)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for pj := range jobCh {
				if ctx.Err() != nil {
					return
				}
				attempted.Add(1)
				job := mediaJob{chatJID: pj.ChatJID, msgID: pj.MsgID}
				// Recover per job so one panic fails a single download
				// instead of killing the worker (mirrors runMediaWorkers, #52).
				func() {
					defer func() {
						if r := recover(); r != nil {
							failed.Add(1)
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
					ok, err := a.downloadMediaJob(ctx, job)
					switch {
					case err != nil:
						failed.Add(1)
						a.emitWarning(
							"media_download_failed",
							fmt.Sprintf("media download failed for %s/%s: %v", job.chatJID, job.msgID, err),
							map[string]any{"chat_jid": job.chatJID, "msg_id": job.msgID, "error": err.Error()},
						)
					case ok:
						downloaded.Add(1)
					default:
						skipped.Add(1)
					}
				}()
			}
		}()
	}

	feed := func() {
		defer close(jobCh)
		for _, job := range jobs {
			if ctx.Err() != nil {
				return
			}
			select {
			case <-ctx.Done():
				return
			case jobCh <- job:
			}
		}
	}
	feed()
	wg.Wait()

	result.Attempted = int(attempted.Load())
	result.Downloaded = int(downloaded.Load())
	result.Skipped = int(skipped.Load())
	result.Failed = int(failed.Load())

	a.emitEvent("media_backfill_done", map[string]any{
		"pending":    result.Pending,
		"attempted":  result.Attempted,
		"downloaded": result.Downloaded,
		"skipped":    result.Skipped,
		"failed":     result.Failed,
	})

	return result, ctx.Err()
}

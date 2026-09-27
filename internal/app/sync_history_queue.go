package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// A RECENT or FULL history chunk can hold tens of thousands of messages and
// take minutes to store. Stored inside the event handler, it held up every
// event behind it: live messages, receipts and the offline backlog all waited
// for the chunk to finish. These chunks are queued in the store instead and
// stored in order by one worker, so the handler returns at once.
//
// The queue is kept in the store, not in memory, because whatsmeow
// acknowledges the notification as soon as it arrives: a chunk held only in
// memory would be lost for good if the process stopped before storing it,
// while a queued one is picked up by the next sync.
//
// INITIAL_BOOTSTRAP and the small chunk types are still stored in line. The
// bootstrap carries the phone's unread state, which has to land before the
// live events that follow it; a queued chunk is stored later, when that state
// may have moved on, so its unread counts are not applied.
//
// Because a row outlives the session that queued it, each one records the
// linked account it was received for. A row for any other account, as after
// the store is logged out and paired with a different one, is dropped before
// anything is downloaded or written.

const historyQueueMaxAttempts = 3

// historyQueueRetryDelay is how long the worker waits before trying again a
// chunk whose download failed. Tests shorten it.
var historyQueueRetryDelay = 30 * time.Second

// historyQueueReadRetryDelay is how long the worker waits before reading the
// queue again after a failed read. Tests shorten it.
var historyQueueReadRetryDelay = time.Second

// historyQueuePoll is how often waitIdle looks at the queue.
var historyQueuePoll = 50 * time.Millisecond

var errHistoryQueueUnreadable = errors.New("queued history sync notification cannot be read")

func queuesHistorySync(syncType waE2E.HistorySyncType) bool {
	return syncType == waE2E.HistorySyncType_RECENT || syncType == waE2E.HistorySyncType_FULL
}

// historyQueueStore is the part of the store the queue uses.
type historyQueueStore interface {
	EnqueueHistorySync(msgID, accountJID string, syncType int32, notification []byte, queuedAt time.Time) error
	NextHistorySync(skip []int64) (store.HistorySyncQueueItem, bool, error)
	MarkHistorySyncAttempt(id int64) (int, error)
	DeleteHistorySync(id int64) error
	CountHistorySyncQueue() (int, error)
}

type historyQueue struct {
	a    *App
	db   historyQueueStore
	wake chan struct{}

	mu      sync.Mutex
	working bool
	// retryAt holds the chunks that failed and when they may be tried again.
	// Until then the worker passes over them, so one bad chunk does not hold
	// up the healthy ones queued behind it.
	retryAt map[int64]time.Time
	// unreadable is set once the queue could not be read historyQueueMaxAttempts
	// times in a row; a one-shot sync then stops waiting for it.
	unreadable bool
}

func newHistoryQueue(a *App) *historyQueue {
	return &historyQueue{a: a, db: a.db, wake: make(chan struct{}, 1), retryAt: map[int64]time.Time{}}
}

// enqueue stores the notification in the queue and wakes the worker. It does
// no network work and no parsing, so it is cheap enough for the handler.
func (q *historyQueue) enqueue(msgID string, notif *waE2E.HistorySyncNotification) error {
	account := strings.TrimSpace(q.a.wa.LinkedJID())
	if account == "" {
		return fmt.Errorf("no linked account to queue history sync for")
	}
	raw, err := proto.Marshal(notif)
	if err != nil {
		return fmt.Errorf("encode history sync notification: %w", err)
	}
	if err := q.db.EnqueueHistorySync(msgID, account, int32(notif.GetSyncType()), raw, nowUTC()); err != nil {
		return err
	}
	queued, _ := q.db.CountHistorySyncQueue()
	q.a.emitOrPrint("history_sync_queued", map[string]any{
		"sync_type": notif.GetSyncType().String(),
		"queued":    queued,
	}, "History sync chunk queued (%s, %d waiting).\n", notif.GetSyncType(), queued)
	select {
	case q.wake <- struct{}{}:
	default:
	}
	return nil
}

func (q *historyQueue) setWorking(working bool) {
	q.mu.Lock()
	q.working = working
	q.mu.Unlock()
}

// backOff keeps a failed chunk queued but out of the way until its retry.
func (q *historyQueue) backOff(id int64) {
	q.mu.Lock()
	q.retryAt[id] = nowUTC().Add(historyQueueRetryDelay)
	q.mu.Unlock()
}

func (q *historyQueue) forget(id int64) {
	q.mu.Lock()
	delete(q.retryAt, id)
	q.mu.Unlock()
}

// backedOff returns the chunks still waiting for their retry, and how long
// until the first of them is due (0 when there are none).
func (q *historyQueue) backedOff() (ids []int64, nextDue time.Duration) {
	now := nowUTC()
	q.mu.Lock()
	defer q.mu.Unlock()
	for id, at := range q.retryAt {
		wait := at.Sub(now)
		if wait <= 0 {
			continue
		}
		ids = append(ids, id)
		if nextDue == 0 || wait < nextDue {
			nextDue = wait
		}
	}
	return ids, nextDue
}

// settled reports whether the worker has nothing it could store now: it is
// not storing a chunk and every chunk still queued waits for a retry, which a
// one-shot sync does not stay for (those chunks stay queued).
//
// A failed read counts as unsettled, since chunks may still be waiting: the
// worker retries the read a bounded number of times, and only once it has
// given up does a one-shot sync stop waiting (the chunks stay queued).
func (q *historyQueue) settled() bool {
	q.mu.Lock()
	working, unreadable := q.working, q.unreadable
	q.mu.Unlock()
	if working {
		return false
	}
	if unreadable {
		return true
	}
	skip, _ := q.backedOff()
	_, ok, err := q.db.NextHistorySync(skip)
	return err == nil && !ok
}

// waitIdle blocks until the worker has settled or ctx ends.
func (q *historyQueue) waitIdle(ctx context.Context) {
	for !q.settled() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(historyQueuePoll):
		}
	}
}

// start runs the worker until the returned stop function is called, which
// also waits for it to return. A chunk interrupted by stop stays queued.
func (q *historyQueue) start(ctx context.Context, opts SyncOptions, messagesStored, lastEvent *atomic.Int64, enqueueMedia func(string, string), limits *syncStorageLimits) (stop func()) {
	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	opts.deferredHistory = true
	go func() {
		defer close(done)
		q.run(workerCtx, func(ctx context.Context, item store.HistorySyncQueueItem) error {
			return q.a.storeQueuedHistorySync(ctx, opts, item, messagesStored, lastEvent, enqueueMedia, limits)
		})
	}()
	return func() {
		cancel()
		<-done
	}
}

func (q *historyQueue) run(ctx context.Context, storeItem func(context.Context, store.HistorySyncQueueItem) error) {
	defer q.setWorking(false)
	readFailures := 0
	for ctx.Err() == nil {
		skip, nextDue := q.backedOff()
		item, ok, err := q.db.NextHistorySync(skip)
		if err != nil {
			readFailures++
			if readFailures < historyQueueMaxAttempts {
				q.a.emitWarning("history_queue_read_failed",
					fmt.Sprintf("warning: failed to read the history sync queue (attempt %d of %d), trying again in %s: %v",
						readFailures, historyQueueMaxAttempts, historyQueueReadRetryDelay, err),
					map[string]any{"attempts": readFailures, "error": err.Error()})
				select {
				case <-ctx.Done():
					return
				case <-time.After(historyQueueReadRetryDelay):
				}
				continue
			}
			// Given up for now: queued chunks stay for a later read, which the
			// next queued chunk or the next sync brings.
			q.a.emitWarning("history_queue_unreadable",
				fmt.Sprintf("warning: cannot read the history sync queue after %d attempts; what is queued stays for the next sync: %v", readFailures, err),
				map[string]any{"attempts": readFailures, "error": err.Error()})
			q.mu.Lock()
			q.unreadable = true
			q.mu.Unlock()
		} else {
			readFailures = 0
			q.mu.Lock()
			q.unreadable = false
			q.mu.Unlock()
		}
		if err != nil || !ok {
			// Nothing ready: wait for a new chunk, or for a failed one's retry.
			q.setWorking(false)
			var due <-chan time.Time
			if nextDue > 0 {
				due = time.After(nextDue)
			}
			select {
			case <-ctx.Done():
				return
			case <-q.wake:
			case <-due:
			}
			readFailures = 0
			continue
		}

		q.setWorking(true)
		current := strings.TrimSpace(q.a.wa.LinkedJID())
		if current == "" {
			// Not linked right now: nothing to check the row against, so it
			// waits rather than being stored or dropped.
			q.backOff(item.ID)
			continue
		}
		if item.AccountJID != current {
			// Received for a different linked account, or for none this store
			// can vouch for: drop it before any download or write.
			q.a.emitWarning("history_queue_foreign_account",
				fmt.Sprintf("warning: dropped queued history sync %d: it was received for another linked account", item.ID),
				map[string]any{"queue_id": item.ID})
			q.remove(item.ID)
			continue
		}
		err = storeItem(ctx, item)
		if ctx.Err() != nil {
			// Stopped part way: the chunk stays queued and the next sync stores
			// it again from the start, which only rewrites the same rows.
			return
		}
		if err == nil || errors.Is(err, errHistoryQueueUnreadable) {
			if errors.Is(err, errHistoryQueueUnreadable) {
				q.a.emitWarning("history_queue_dropped",
					fmt.Sprintf("warning: dropped queued history sync %d: %v", item.ID, err),
					map[string]any{"queue_id": item.ID, "error": err.Error()})
			}
			q.remove(item.ID)
			continue
		}

		attempts, markErr := q.db.MarkHistorySyncAttempt(item.ID)
		if markErr != nil {
			// The chunk was not stored, so it must not be lost over bookkeeping:
			// keep it queued and try it again later.
			q.a.emitWarning("history_queue_retry",
				fmt.Sprintf("warning: failed to store queued history sync %d and to count the attempt, trying again in %s: %v (%v)",
					item.ID, historyQueueRetryDelay, err, markErr),
				map[string]any{"queue_id": item.ID, "error": err.Error(), "attempt_error": markErr.Error()})
			q.backOff(item.ID)
			continue
		}
		if attempts >= historyQueueMaxAttempts {
			q.a.emitWarning("history_queue_dropped",
				fmt.Sprintf("warning: gave up on queued history sync %d after %d attempt(s): %v", item.ID, attempts, err),
				map[string]any{"queue_id": item.ID, "attempts": attempts, "error": err.Error()})
			q.remove(item.ID)
			continue
		}
		q.a.emitWarning("history_queue_retry",
			fmt.Sprintf("warning: failed to store queued history sync %d (attempt %d of %d), trying again in %s: %v",
				item.ID, attempts, historyQueueMaxAttempts, historyQueueRetryDelay, err),
			map[string]any{"queue_id": item.ID, "attempts": attempts, "error": err.Error()})
		q.backOff(item.ID)
	}
}

// remove takes a stored or abandoned chunk off the queue. If the delete fails
// the chunk is set aside like a failed one, so the worker does not loop on it;
// storing it again later only rewrites the same rows.
func (q *historyQueue) remove(id int64) {
	if err := q.db.DeleteHistorySync(id); err != nil {
		q.a.emitWarning("history_queue_delete_failed",
			fmt.Sprintf("warning: failed to remove history sync %d from the queue: %v", id, err),
			map[string]any{"queue_id": id, "error": err.Error()})
		q.backOff(id)
		return
	}
	q.forget(id)
}

// storeQueuedHistorySync downloads and stores one queued chunk. It returns an
// error only when the chunk should be tried again (or dropped).
func (a *App) storeQueuedHistorySync(ctx context.Context, opts SyncOptions, item store.HistorySyncQueueItem, messagesStored, lastEvent *atomic.Int64, enqueueMedia func(string, string), limits *syncStorageLimits) error {
	notif := &waE2E.HistorySyncNotification{}
	if err := proto.Unmarshal(item.Notification, notif); err != nil {
		return fmt.Errorf("%w: %v", errHistoryQueueUnreadable, err)
	}
	lastEvent.Store(nowUTC().UnixNano())
	data, err := a.wa.DownloadHistorySync(ctx, notif)
	if err != nil {
		return fmt.Errorf("download history sync: %w", err)
	}
	a.handleHistorySync(ctx, opts, &events.HistorySync{Data: data}, messagesStored, lastEvent, enqueueMedia, limits)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := a.wa.DeleteHistorySyncMedia(ctx, notif); err != nil {
		a.emitWarning(
			"history_delete_failed",
			fmt.Sprintf("warning: failed to delete history sync media: %v", err),
			map[string]any{"error": err.Error()},
		)
	}
	return nil
}

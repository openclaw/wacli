package app

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/openclaw/wacli/internal/wa"
)

type syncStorageLimits struct {
	app    *App
	opts   SyncOptions
	cancel context.CancelFunc

	mu                sync.Mutex
	err               error
	persistenceFailed atomic.Bool
}

func (l *syncStorageLimits) StoreParsedMessage(ctx context.Context, pm wa.ParsedMessage) error {
	if l.opts.MaxMessages <= 0 && l.opts.MaxDBSizeBytes <= 0 {
		err := l.app.storeParsedMessage(ctx, pm)
		if err != nil {
			l.persistenceFailed.Store(true)
		}
		return err
	}
	if err := l.app.checkSyncStorageLimits(l.opts); err != nil {
		l.setErr(err)
		return err
	}
	if err := l.app.storeParsedMessage(ctx, pm); err != nil {
		l.persistenceFailed.Store(true)
		return err
	}
	if err := l.app.checkSyncStorageLimits(l.opts); err != nil {
		l.setErr(err)
	}
	return nil
}

func (l *syncStorageLimits) Err() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err
}

func (l *syncStorageLimits) setErr(err error) {
	if l == nil || err == nil {
		return
	}
	l.mu.Lock()
	if l.err == nil {
		l.err = err
		if l.cancel != nil {
			l.cancel()
		}
	}
	l.mu.Unlock()
}

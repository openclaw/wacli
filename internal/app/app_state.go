package app

import "sync"

type appStatePersistenceTracker struct {
	mu     sync.Mutex
	active bool
	err    error
}

func (t *appStatePersistenceTracker) begin() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.active = true
	t.err = nil
}

func (t *appStatePersistenceTracker) record(err error) {
	if err == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.active && t.err == nil {
		t.err = err
	}
}

func (t *appStatePersistenceTracker) end() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.active = false
	return t.err
}

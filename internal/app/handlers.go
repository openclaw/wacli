package app

import "sync/atomic"

// Event handlers that the app registers for a command go through
// addEventHandler and removeEventHandler.
//
// whatsmeow's RemoveEventHandler takes the write lock that dispatchEvent holds
// while handlers run, so it waits for the event being dispatched. For a
// command that connected by itself that event can be the one-shot store's
// (a history download, a write in SQLite's busy wait), and a removal on the
// command's way out would hold the command past its deadline, before the
// bounded wait in Close. So while a one-shot store exists (from App.Connect
// to the end of Close), removeEventHandler marks the handler removed, and its
// function is not called again, but leaves the client's own removal to Close,
// after the bounded wait. Without a one-shot store (a running sync, the
// commands it runs for others, a command that never connects) it removes the
// handler at once and, as before, waits for a call in progress.
//
// What a caller can rely on once removeEventHandler returns: its function is
// not called again. What it can no longer rely on while a one-shot store
// exists: that a call already in progress has returned. The handlers removed
// this way guard their own state with their own lock.
type appEventHandler struct {
	removed atomic.Bool
}

func (a *App) addEventHandler(fn func(any)) uint32 {
	h := &appEventHandler{}
	id := a.wa.AddEventHandler(func(evt any) {
		if !h.removed.Load() {
			fn(evt)
		}
	})
	a.handlersMu.Lock()
	if a.handlers == nil {
		a.handlers = make(map[uint32]*appEventHandler)
	}
	a.handlers[id] = h
	a.handlersMu.Unlock()
	return id
}

func (a *App) removeEventHandler(waClient WAClient, id uint32) {
	a.handlersMu.Lock()
	if h := a.handlers[id]; h != nil {
		h.removed.Store(true)
		delete(a.handlers, id)
	}
	deferred := a.deferRemovals
	if deferred {
		a.deferredRemovals = append(a.deferredRemovals, id)
	}
	a.handlersMu.Unlock()
	if !deferred {
		waClient.RemoveEventHandler(id)
	}
}

// deferHandlerRemovals starts deferring the client's removals to Close
// (startOneShotStore).
func (a *App) deferHandlerRemovals() {
	a.handlersMu.Lock()
	a.deferRemovals = true
	a.handlersMu.Unlock()
}

// finishHandlerRemovals runs in Close after the bounded wait: it removes the
// deferred handlers when no handler is still running an event, and otherwise
// leaves them registered, inert, for the process to exit with.
func (a *App) finishHandlerRemovals(waClient WAClient, idle bool) {
	a.handlersMu.Lock()
	ids := a.deferredRemovals
	a.deferredRemovals = nil
	a.deferRemovals = false
	a.handlersMu.Unlock()
	if !idle || waClient == nil {
		return
	}
	for _, id := range ids {
		waClient.RemoveEventHandler(id)
	}
}

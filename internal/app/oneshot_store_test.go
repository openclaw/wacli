package app

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/sqliteutil"
	"github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/wa"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// A command that runs by itself connects through App.Connect; what the server
// delivers to that connection reaches every registered handler, as whatsmeow
// dispatches it, before whatsmeow acknowledges it. The transport proof is
// cmd/wacli/offline_backlog_transport_test.go; these cover the other kinds.

func oneShotTestApp(t *testing.T) (*App, *fakeWA, *bytes.Buffer) {
	t.Helper()
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	var evbuf bytes.Buffer
	a.opts.Events = out.NewEventWriter(&evbuf, true)
	if err := a.Connect(t.Context(), false, nil); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return a, f, &evbuf
}

func (f *fakeWA) handlerCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.handlers)
}

func oneShotChat() types.JID { return types.NewJID("15551234567", types.DefaultUserServer) }

func oneShotIncoming(id string, at time.Time) *events.Message {
	chat := oneShotChat()
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat},
			ID:            types.MessageID(id),
			Timestamp:     at,
			PushName:      "Alice",
		},
		Message: &waE2E.Message{Conversation: proto.String("hello " + id)},
	}
}

func TestOneShotConnectionStoresHistoryChunk(t *testing.T) {
	a, f, _ := oneShotTestApp(t)
	at := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	f.downloadHistory = func(*waE2E.HistorySyncNotification) (*waHistorySync.HistorySync, error) {
		return historySyncWithTextMessages(oneShotChat(), at, "h1", "h2").Data, nil
	}
	own := types.NewJID("15550000000", types.DefaultUserServer)
	syncType := waE2E.HistorySyncType_RECENT
	f.emit(&events.Message{
		Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: own, Sender: own, IsFromMe: true}, ID: "hist-notif", Timestamp: at},
		Message: &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
			HistorySyncNotification: &waE2E.HistorySyncNotification{SyncType: &syncType}}},
	})
	for _, id := range []string{"h1", "h2"} {
		if _, err := a.db.GetMessage(oneShotChat().String(), id); err != nil {
			t.Fatalf("history message %s not stored by the one-shot connection: %v", id, err)
		}
	}
	f.mu.Lock()
	manual, deleted := append([]bool(nil), f.manualHistorySyncCalls...), len(f.deleteHistoryCalls)
	f.mu.Unlock()
	if len(manual) == 0 || !manual[0] || deleted != 1 {
		t.Fatalf("manual history download calls = %v, server copies deleted = %d; want manual download on before connecting, and the copy deleted once stored", manual, deleted)
	}
}

func TestOneShotConnectionStoresCallEvents(t *testing.T) {
	a, f, _ := oneShotTestApp(t)
	remote := oneShotChat()
	f.emit(&events.CallOffer{
		BasicCallMeta: types.BasicCallMeta{From: remote, CallCreator: remote, CallID: "call-oneshot-1", Timestamp: time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)},
		Data:          &waBinary.Node{Attrs: waBinary.Attrs{"media": "audio"}},
	})
	calls, err := a.db.ListCallEvents(store.ListCallEventsParams{ChatJID: remote.String(), Limit: 10})
	if err != nil || len(calls) != 1 || calls[0].CallID != "call-oneshot-1" {
		t.Fatalf("call events = %+v, %v; want the offer the one-shot connection received", calls, err)
	}
}

func TestOneShotConnectionStoresAppStateChanges(t *testing.T) {
	a, f, _ := oneShotTestApp(t)
	chat := oneShotChat()
	if err := a.db.UpsertChat(chat.String(), "dm", "Alice", time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	f.emit(&events.Archive{JID: chat, Action: &waSyncAction.ArchiveChatAction{Archived: proto.Bool(true)}})
	if err := a.appStatePersist.waitIdle(t.Context()); err != nil {
		t.Fatal(err)
	}
	if stored, err := a.db.GetChat(chat.String()); err != nil || !stored.Archived {
		t.Fatalf("chat after an archive change delivered to the one-shot connection = %+v, %v; want archived", stored, err)
	}
}

// A storage failure is reported when the command closes, not swallowed.
func TestOneShotConnectionReportsWhatItCouldNotStore(t *testing.T) {
	a, f, evbuf := oneShotTestApp(t)
	_ = a.db.Close() // every write fails from here on
	f.emit(oneShotIncoming("m-unstored", time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)))
	a.Close()
	if !strings.Contains(evbuf.String(), `"code":"oneshot_store_failed"`) {
		t.Fatalf("no oneshot_store_failed warning after a delivered message failed to store:\n%s", evbuf.String())
	}
}

// The connection's own lifecycle is the command's business: no connected or
// offline-backlog output, no freshness record, from the store handler.
func TestOneShotConnectionLeavesSyncLifecycleAlone(t *testing.T) {
	a, f, evbuf := oneShotTestApp(t)
	f.emit(&events.OfflineSyncPreview{Total: 1, Messages: 1})
	f.emit(&events.OfflineSyncCompleted{Count: 1})
	for _, event := range []string{`"event":"connected"`, `"event":"offline_sync_preview"`, `"event":"offline_sync_completed"`} {
		if strings.Contains(evbuf.String(), event) {
			t.Fatalf("one-shot connection emitted %s:\n%s", event, evbuf.String())
		}
	}
	if last, err := a.db.LastSuccessfulSync(); err != nil || last != nil {
		t.Fatalf("one-shot connection recorded a successful sync at %v (%v)", last, err)
	}
}

// Reconnects (send retries disconnect and connect again) keep one handler.
func TestOneShotReconnectKeepsOneStoreHandler(t *testing.T) {
	a, f, _ := oneShotTestApp(t)
	before := f.handlerCount()
	f.Disconnect()
	if err := a.Connect(t.Context(), false, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.handlerCount(); got != before {
		t.Fatalf("handlers after a reconnect = %d, want %d", got, before)
	}
	a.Close()
	if got := f.handlerCount(); got != 0 {
		t.Fatalf("handlers after Close = %d, want 0", got)
	}
}

// Commands a running sync executes for others (the delegate socket) reconnect
// through App.Connect; sync's handler already stores what arrives.
func TestSyncReconnectForDelegatedCommandAddsNoStoreHandler(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	var during, after int
	_, err := a.Sync(t.Context(), SyncOptions{Mode: SyncModeOnce, IdleExit: 50 * time.Millisecond, PresenceMode: SyncPresenceModeQuiet,
		AfterConnect: func(ctx context.Context) error {
			before := f.handlerCount()
			f.Disconnect()
			if err := a.Connect(ctx, false, nil); err != nil {
				return err
			}
			during, after = before, f.handlerCount()
			return nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	if after != during {
		t.Fatalf("handlers after a delegated reconnect inside sync = %d, want %d (no second store handler)", after, during)
	}
}

// Chat-state commands and contacts save persist app state with their own
// handler; the store handler leaves app state to it.
func TestOneShotStoreLeavesAppStateToChatStatePersister(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	var evbuf bytes.Buffer
	a.opts.Events = out.NewEventWriter(&evbuf, true)
	remove, err := a.AddChatStatePersistenceHandler(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer remove()
	if err := a.Connect(t.Context(), false, nil); err != nil {
		t.Fatal(err)
	}
	f.emit(&wa.AppStateKeyUnavailable{KeyID: []byte{0xAB}})
	if n := strings.Count(evbuf.String(), `"code":"app_state_key_unavailable"`); n != 1 {
		t.Fatalf("app-state warnings = %d, want 1 (handled once):\n%s", n, evbuf.String())
	}
}

// A one-shot command prints no sync progress for what it stores: the line
// has no newline and would be glued to the command's next stderr line.
func TestOneShotConnectionPrintsNoSyncProgress(t *testing.T) {
	a, f, evbuf := oneShotTestApp(t)
	at := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	for i := range 25 {
		f.emit(oneShotIncoming(fmt.Sprintf("m-%02d", i), at.Add(time.Duration(i)*time.Second)))
	}
	if n, err := a.db.CountMessages(); err != nil || n != 25 {
		t.Fatalf("messages stored = %d, %v; want 25", n, err)
	}
	if strings.Contains(evbuf.String(), `"event":"progress"`) {
		t.Fatalf("one-shot connection emitted sync progress:\n%s", evbuf.String())
	}
}

// A history chunk still being imported when the command closes: the wait at
// exit ends at the command's own deadline, and the warning says what was lost.
func TestOneShotExitWaitEndsAtTheCommandDeadline(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	var evbuf bytes.Buffer
	a.opts.Events = out.NewEventWriter(&evbuf, true)
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	if err := a.Connect(ctx, false, nil); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	entered := make(chan struct{})
	at := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	f.downloadHistory = func(*waE2E.HistorySyncNotification) (*waHistorySync.HistorySync, error) {
		close(entered)
		<-release
		return historySyncWithTextMessages(oneShotChat(), at, "h1").Data, nil
	}
	own := types.NewJID("15550000000", types.DefaultUserServer)
	syncType := waE2E.HistorySyncType_RECENT
	notif := &events.Message{
		Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: own, Sender: own, IsFromMe: true}, ID: "hist-slow", Timestamp: at},
		Message: &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
			HistorySyncNotification: &waE2E.HistorySyncNotification{SyncType: &syncType}}},
	}
	dispatched := make(chan struct{})
	go func() { f.emit(notif); close(dispatched) }()
	<-entered
	start := time.Now()
	s := a.fenceOneShotStore()
	waited := time.Since(start)
	close(release)
	<-dispatched
	if waited > time.Second {
		t.Fatalf("the wait at exit took %s; it must end at the command's deadline (300ms after connecting)", waited)
	}
	s.finish(a)
	if !strings.Contains(evbuf.String(), `"code":"oneshot_store_incomplete"`) || !strings.Contains(evbuf.String(), "will not deliver it again") {
		t.Fatalf("no oneshot_store_incomplete warning saying the cut-short event is lost:\n%s", evbuf.String())
	}
}

// dispatchLockingWA models whatsmeow's handler lock: dispatching an event holds
// it for reading while the handlers run, and RemoveEventHandler takes it for
// writing, so removing a handler waits for the event being dispatched.
type dispatchLockingWA struct {
	*fakeWA
	dispatch sync.RWMutex
}

func (w *dispatchLockingWA) emit(evt any) {
	w.dispatch.RLock()
	defer w.dispatch.RUnlock()
	w.fakeWA.emit(evt)
}

func (w *dispatchLockingWA) RemoveEventHandler(id uint32) {
	w.dispatch.Lock()
	defer w.dispatch.Unlock()
	w.fakeWA.RemoveEventHandler(id)
}

// closeWithin runs a.Close and fails if it has not returned within limit; it
// then releases whatever blocks it, so the test ends.
func closeWithin(t *testing.T, a *App, limit time.Duration, unblock func()) time.Duration {
	t.Helper()
	start := time.Now()
	done := make(chan time.Duration, 1)
	go func() { a.Close(); done <- time.Since(start) }()
	select {
	case took := <-done:
		return took
	case <-time.After(limit):
		stacks := make([]byte, 1<<20)
		t.Logf("Close still running after %s; goroutines:\n%s", limit, stacks[:runtime.Stack(stacks, true)])
		unblock()
		took := <-done
		t.Fatalf("Close returned only after the blocked store was released (%s): it waited for the event being stored, whatever the command's deadline", took)
		return took
	}
}

// Through App.Close: an item still being stored at exit by a store that does
// not return in time (here a history download that ignores its context).
// Close must return at the command's deadline and report the loss.
func TestOneShotCloseReturnsAtTheCommandDeadlineWhileAStoreHangs(t *testing.T) {
	a := newTestApp(t)
	w := &dispatchLockingWA{fakeWA: newFakeWA()}
	a.wa = w
	var evbuf bytes.Buffer
	a.opts.Events = out.NewEventWriter(&evbuf, true)
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	if err := a.Connect(ctx, false, nil); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	entered := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	at := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	w.downloadHistory = func(*waE2E.HistorySyncNotification) (*waHistorySync.HistorySync, error) {
		close(entered)
		<-release
		return historySyncWithTextMessages(oneShotChat(), at, "h1").Data, nil
	}
	own := types.NewJID("15550000000", types.DefaultUserServer)
	syncType := waE2E.HistorySyncType_RECENT
	go w.emit(&events.Message{
		Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: own, Sender: own, IsFromMe: true}, ID: "hist-hangs", Timestamp: at},
		Message: &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
			HistorySyncNotification: &waE2E.HistorySyncNotification{SyncType: &syncType}}},
	})
	<-entered
	// A Close that waits for the store never returns by itself; the timing
	// bound gets slack for a loaded machine (-race, parallel packages).
	took := closeWithin(t, a, 10*time.Second, unblock)
	if took > 3*time.Second {
		t.Fatalf("Close took %s; the command's deadline was 300ms after connecting", took)
	}
	if !strings.Contains(evbuf.String(), `"code":"oneshot_store_incomplete"`) {
		t.Fatalf("no oneshot_store_incomplete warning for the item cut short:\n%s", evbuf.String())
	}
	t.Logf("Close returned %s after it was called, with the store still blocked", took.Round(time.Millisecond))
}

// Through App.Close, with a real SQLite statement in flight: another connection
// holds the write lock on wacli.db, so the handler's write waits in SQLite's
// busy handler (wacli.db: 5 s busy timeout). Close must return at the
// command's deadline: closing wacli.db does not wait for a statement in use.
func TestOneShotCloseDoesNotWaitForAStatementInBusyWait(t *testing.T) {
	a := newTestApp(t)
	w := &dispatchLockingWA{fakeWA: newFakeWA()}
	a.wa = w
	var evbuf bytes.Buffer
	a.opts.Events = out.NewEventWriter(&evbuf, true)
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	if err := a.Connect(ctx, false, nil); err != nil {
		t.Fatal(err)
	}
	locker, err := sql.Open("sqlite3", sqliteutil.FileURI(filepath.Join(a.StoreDir(), "wacli.db"), "_busy_timeout=5000"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = locker.Close() })
	tx, err := locker.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO chats(jid, kind, name, last_message_ts) VALUES ('locker@s.whatsapp.net', 'dm', 'locker', 0)`); err != nil {
		t.Fatal(err)
	}
	var lockOnce sync.Once
	unlock := func() { lockOnce.Do(func() { _ = tx.Rollback() }) }
	t.Cleanup(unlock)
	handled := make(chan time.Time, 1)
	go func() {
		w.emit(oneShotIncoming("m-busy", time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)))
		handled <- time.Now()
	}()
	time.Sleep(100 * time.Millisecond) // the handler's write is now in the busy wait
	start := time.Now()
	// The busy wait would end by itself after 5 s; within that, a Close that
	// waited for the statement would still be running at the limit.
	took := closeWithin(t, a, 4*time.Second, unlock)
	if took > 3*time.Second {
		t.Fatalf("Close took %s with a statement in busy wait; the command's deadline was 300ms after connecting", took)
	}
	if !strings.Contains(evbuf.String(), `"code":"oneshot_store_incomplete"`) {
		t.Fatalf("no oneshot_store_incomplete warning for the message cut short:\n%s", evbuf.String())
	}
	select {
	case at := <-handled:
		t.Logf("the handler returned %s after Close began, before Close returned", at.Sub(start).Round(time.Millisecond))
	default:
		t.Logf("Close returned %s after it began while the handler's write was still in the busy wait; wacli.db was closed under it", took.Round(time.Millisecond))
	}
	unlock()
	select {
	case at := <-handled:
		t.Logf("the handler returned %s after Close began, once the lock was released", at.Sub(start).Round(time.Millisecond))
	case <-time.After(6 * time.Second):
		t.Fatal("the handler did not return within the busy timeout after the lock was released")
	}
}

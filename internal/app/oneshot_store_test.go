package app

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/out"
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

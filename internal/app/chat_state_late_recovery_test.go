package app

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// lateRecoveryTestApp drives a recovery attempt whose full sync fails and
// whose primary does not answer within the waiter's timeout, leaving a
// background continuation behind.
func lateRecoveryTestApp(t *testing.T, chat types.JID) (*App, *appStateContextWA) {
	t.Helper()
	a := newTestApp(t)
	if err := a.db.UpsertChat(chat.String(), "dm", "Synthetic", time.Now()); err != nil {
		t.Fatal(err)
	}
	f := &appStateContextWA{fakeWA: newFakeWA()}
	f.fetchEvents = func(context.Context, string, bool, bool) ([]any, error) {
		return nil, appstate.ErrMismatchingLTHash
	}
	f.requestAppStateRecovery = func(context.Context, string) (types.MessageID, error) {
		return types.MessageID("recovery-req"), nil
	}
	a.wa = f
	return a, f
}

func runTimedOutRecoveryAttempt(t *testing.T, a *App, name string) {
	t.Helper()
	var budget sync.Map
	budget.Store(name, struct{}{})
	a.recoverAppStateCollection(t.Context(), name, &budget, 50*time.Millisecond)
	if required, err := a.db.AppStateRecoveryRequired(name); err != nil || !required {
		t.Fatalf("recovery intent after timed out attempt = %v, %v; want true", required, err)
	}
}

func fakeHandlerCount(f *appStateContextWA) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.handlers)
}

func countRecoveryIntents(t *testing.T, a *App, name string) (count int, maxID int64) {
	t.Helper()
	raw, err := sql.Open("sqlite3", filepath.Join(a.StoreDir(), "wacli.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if err := raw.QueryRow(
		`SELECT COUNT(*), COALESCE(MAX(id), 0) FROM app_state_recovery_intents WHERE collection = ?`, name,
	).Scan(&count, &maxID); err != nil {
		t.Fatal(err)
	}
	return count, maxID
}

func TestLateAppStateRecoveryPersistsAndClearsMarkers(t *testing.T) {
	chat := types.JID{User: "15550000010", Server: types.DefaultUserServer}
	a, f := lateRecoveryTestApp(t, chat)
	name := string(appstate.WAPatchRegularLow)
	// Stale markers from older failed attempts.
	for range 2 {
		if _, err := a.db.MarkAppStateRecoveryGeneration(name); err != nil {
			t.Fatal(err)
		}
	}

	runTimedOutRecoveryAttempt(t, a, name)

	// The phone answers late: whatsmeow adopts the snapshot and dispatches its
	// events followed by the recovery completion sentinel.
	f.emit(&events.Archive{JID: chat, Action: &waSyncAction.ArchiveChatAction{Archived: proto.Bool(true)}})
	f.emit(&events.AppStateSyncComplete{Name: appstate.WAPatchRegularLow, Recovery: true})

	waitForCondition(t, 5*time.Second, func() bool {
		stored, err := a.db.GetChat(chat.String())
		if err != nil || !stored.Archived {
			return false
		}
		required, err := a.db.AppStateRecoveryRequired(name)
		return err == nil && !required
	})
	if stored, err := a.db.GetChat(chat.String()); err != nil || !stored.Archived {
		t.Fatalf("late recovery did not persist archive flag: %+v, %v", stored, err)
	}
	if required, err := a.db.AppStateRecoveryRequired(name); err != nil || required {
		t.Fatalf("late recovery did not clear markers: %v, %v", required, err)
	}
}

func TestLateAppStateRecoveryKeepsMarkersNewerThanAttempt(t *testing.T) {
	chat := types.JID{User: "15550000011", Server: types.DefaultUserServer}
	a, f := lateRecoveryTestApp(t, chat)
	name := string(appstate.WAPatchRegularLow)

	runTimedOutRecoveryAttempt(t, a, name)

	// Another write marks new replay debt after the attempt's generation.
	newerGen, err := a.db.MarkAppStateRecoveryGeneration(name)
	if err != nil {
		t.Fatal(err)
	}

	f.emit(&events.Archive{JID: chat, Action: &waSyncAction.ArchiveChatAction{Archived: proto.Bool(true)}})
	f.emit(&events.AppStateSyncComplete{Name: appstate.WAPatchRegularLow, Recovery: true})

	waitForCondition(t, 5*time.Second, func() bool {
		stored, err := a.db.GetChat(chat.String())
		return err == nil && stored.Archived
	})
	waitForCondition(t, 5*time.Second, func() bool {
		count, _ := countRecoveryIntents(t, a, name)
		return count == 1
	})
	count, maxID := countRecoveryIntents(t, a, name)
	if count != 1 || maxID != newerGen {
		t.Fatalf("markers after late completion = %d (max id %d), want only the newer marker %d", count, maxID, newerGen)
	}
	if required, err := a.db.AppStateRecoveryRequired(name); err != nil || !required {
		t.Fatalf("newer marker was cleared by an older attempt: %v, %v", required, err)
	}
}

func TestLateAppStateRecoveryContinuationStopsOnClose(t *testing.T) {
	chat := types.JID{User: "15550000012", Server: types.DefaultUserServer}
	a, f := lateRecoveryTestApp(t, chat)
	name := string(appstate.WAPatchRegularLow)

	runTimedOutRecoveryAttempt(t, a, name)

	// The continuation must keep the capture's event handler registered while
	// the app is running, so a late answer is not lost.
	if got := fakeHandlerCount(f); got != 1 {
		t.Fatalf("event handlers after timed out attempt = %d, want 1 (late recovery capture)", got)
	}

	done := make(chan struct{})
	go func() {
		a.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("App.Close did not stop the late recovery continuation")
	}

	// No leaked continuation: the capture's handler is gone and a late answer
	// after close is ignored without panicking on the closed database.
	if got := fakeHandlerCount(f); got != 0 {
		t.Fatalf("event handlers after close = %d, want 0", got)
	}
	f.emit(&events.AppStateSyncComplete{Name: appstate.WAPatchRegularLow, Recovery: true})
}

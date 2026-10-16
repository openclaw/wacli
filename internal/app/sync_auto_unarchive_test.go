package app

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestLiveIncomingUnarchivesAndSurvivesArchiveReplay(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	ctx := context.Background()
	chat := types.NewJID("15550000001", types.DefaultUserServer)
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	if err := a.db.UpsertChat(chat.String(), "dm", "Synthetic", base); err != nil {
		t.Fatal(err)
	}
	setting := func(enabled bool, ts time.Time) {
		a.handleAppStatePersistenceEvent(ctx, &events.UnarchiveChatsSetting{
			Timestamp: ts,
			Action:    &waSyncAction.UnarchiveChatsSetting{UnarchiveChats: proto.Bool(enabled)},
		}, nil)
	}
	archive := func(ts time.Time) {
		a.handleAppStatePersistenceEvent(ctx, &events.Archive{
			JID: chat, Timestamp: ts, FromFullSync: true,
			Action: &waSyncAction.ArchiveChatAction{Archived: proto.Bool(true)},
		}, nil)
	}
	assertArchived := func(want bool) {
		t.Helper()
		got, err := a.db.GetChat(chat.String())
		if err != nil || got.Archived != want {
			t.Fatalf("archived = %v, want %v (error %v)", got.Archived, want, err)
		}
	}
	var stored atomic.Int64
	message := func(id string, ts time.Time, own bool) {
		a.handleLiveSyncMessage(ctx, SyncOptions{}, &events.Message{
			Info: types.MessageInfo{
				MessageSource: types.MessageSource{Chat: chat, Sender: chat, IsFromMe: own},
				ID:            id, Timestamp: ts,
			},
			Message: &waE2E.Message{Conversation: proto.String("synthetic arrival")},
		}, &stored, nil, nil)
	}
	setting(true, base)
	archive(base.Add(time.Second))
	message("new", base.Add(2*time.Second), false)
	assertArchived(false)
	archive(base.Add(time.Second))
	assertArchived(false)
	// A later explicit archive wins, and duplicate/own messages cannot undo it.
	archive(base.Add(3 * time.Second))
	message("new", base.Add(2*time.Second), false)
	message("own", base.Add(4*time.Second), true)
	assertArchived(true)
	// Keep-chats-archived wins even when an older preference is replayed.
	setting(false, base.Add(5*time.Second))
	setting(true, base)
	message("kept", base.Add(6*time.Second), false)
	assertArchived(true)
	setting(true, base.Add(7*time.Second))
	assertArchived(true)
	message("allowed", base.Add(8*time.Second), false)
	assertArchived(false)
	setting(false, base.Add(9*time.Second))
	archive(base.Add(3 * time.Second))
	assertArchived(false)
}

func TestAutoUnarchiveRecoveryCompletesWithoutBrokenFullReplay(t *testing.T) {
	a := newTestApp(t)
	f := &appStateContextWA{fakeWA: newFakeWA()}
	a.wa = f
	ctx := context.Background()
	chat := types.NewJID("15550000003", types.DefaultUserServer)
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	if err := a.db.UpsertChat(chat.String(), "dm", "Synthetic", base); err != nil {
		t.Fatal(err)
	}
	if err := a.db.SetChatArchived(chat.String(), true); err != nil {
		t.Fatal(err)
	}
	a.setAppStateFullSyncResult(appstate.WAPatchRegularLow, true)
	var messagesStored atomic.Int64
	a.handleLiveSyncMessage(ctx, SyncOptions{}, &events.Message{
		Info:    types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: chat}, ID: "pending", Timestamp: base.Add(2 * time.Second)},
		Message: &waE2E.Message{Conversation: proto.String("synthetic")},
	}, &messagesStored, nil, nil)
	requests := 0
	incremental := 0
	f.requestAppStateRecovery = func(context.Context, string) (types.MessageID, error) { requests++; return "snapshot", nil }
	f.fetchEvents = func(_ context.Context, name string, full, _ bool) ([]any, error) {
		if name == string(appstate.WAPatchRegularLow) && full {
			t.Error("destructive full replay on known-broken collection")
		}
		if name == string(appstate.WAPatchRegularLow) && !full {
			incremental++
		}
		return nil, nil
	}
	f.fetchAppState = func(ctx context.Context, name string, full, onlyIfNotSynced bool) error {
		_, err := f.fetchEvents(ctx, name, full, onlyIfNotSynced)
		return err
	}
	var recovering sync.Map
	a.syncAppStateDeltas(ctx, &recovering)
	if requests != 1 {
		t.Fatalf("snapshot requests=%d, want 1", requests)
	}
	if incremental != 1 {
		t.Fatalf("incremental catch-ups=%d, want 1 while snapshot pending", incremental)
	}
	if required, err := a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularLow)); err != nil || !required {
		t.Fatalf("incremental incorrectly cleared snapshot debt: %v, %v", required, err)
	}
	f.emit(&events.UnarchiveChatsSetting{Timestamp: base, Action: &waSyncAction.UnarchiveChatsSetting{UnarchiveChats: proto.Bool(true)}, FromFullSync: true})
	f.emit(&events.Archive{JID: chat, Timestamp: base.Add(time.Second), Action: &waSyncAction.ArchiveChatAction{Archived: proto.Bool(true)}, FromFullSync: true})
	f.emit(&events.AppStateSyncComplete{Name: appstate.WAPatchRegularLow, Recovery: true})
	waitForCondition(t, time.Second, func() bool {
		pending, err := a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularLow))
		return err == nil && !pending
	})
	got, err := a.db.GetChat(chat.String())
	if err != nil || got.Archived {
		t.Fatalf("recovered arrival=%+v, %v", got, err)
	}
	var next sync.Map
	a.syncAppStateDeltas(ctx, &next)
	if requests != 1 {
		t.Fatalf("completed snapshot requested again: %d", requests)
	}
}

func TestAutoUnarchiveWaitsForPreferenceAndArchiveBoundary(t *testing.T) {
	for _, preferenceFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "archive first", true: "preference first"}[preferenceFirst], func(t *testing.T) {
			a := newTestApp(t)
			a.wa = newFakeWA()
			ctx := context.Background()
			chat := types.NewJID("15550000002", types.DefaultUserServer)
			base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
			if err := a.db.UpsertChat(chat.String(), "dm", "Synthetic", base); err != nil {
				t.Fatal(err)
			}
			if err := a.db.SetChatArchived(chat.String(), true); err != nil {
				t.Fatal(err)
			}
			var stored atomic.Int64
			a.handleLiveSyncMessage(ctx, SyncOptions{}, &events.Message{
				Info:    types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: chat}, ID: "pending", Timestamp: base.Add(2 * time.Second)},
				Message: &waE2E.Message{Conversation: proto.String("pending preference recovery")},
			}, &stored, nil, nil)
			assert := func(want bool) {
				t.Helper()
				got, err := a.db.GetChat(chat.String())
				if err != nil || got.Archived != want {
					t.Fatalf("archived=%v, want %v; %v", got.Archived, want, err)
				}
			}
			assert(true)
			pref := &events.UnarchiveChatsSetting{Timestamp: base, Action: &waSyncAction.UnarchiveChatsSetting{UnarchiveChats: proto.Bool(true)}, FromFullSync: true}
			arch := &events.Archive{JID: chat, Timestamp: base.Add(time.Second), Action: &waSyncAction.ArchiveChatAction{Archived: proto.Bool(true)}, FromFullSync: true}
			first, second := any(arch), any(pref)
			if preferenceFirst {
				first, second = pref, arch
			}
			a.handleAppStatePersistenceEvent(ctx, first, nil)
			assert(true)
			a.handleAppStatePersistenceEvent(ctx, second, nil)
			assert(false)
			// Shared history ingestion must not act as a fresh arrival.
			a.handleAppStatePersistenceEvent(ctx, &events.Archive{JID: chat, Timestamp: base.Add(3 * time.Second), Action: &waSyncAction.ArchiveChatAction{Archived: proto.Bool(true)}}, nil)
			if err := a.storeParsedMessage(ctx, wa.ParsedMessage{Chat: chat, ID: "history", Timestamp: base.Add(4 * time.Second), Text: "history"}, messageStoreOptions{}); err != nil {
				t.Fatal(err)
			}
			assert(true)
		})
	}
}

// An upgraded store whose complete regular_low snapshot does not mention a
// locally archived chat, or carries no Keep chats archived mutation, must not
// request the full replay again on every start.
func TestAutoUnarchiveRecoverySettlesAfterCompleteReplay(t *testing.T) {
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	chat := types.NewJID("15550000004", types.DefaultUserServer)
	preference := &events.UnarchiveChatsSetting{
		Timestamp: base, Action: &waSyncAction.UnarchiveChatsSetting{UnarchiveChats: proto.Bool(true)},
	}
	for name, preferenceKnown := range map[string]bool{
		"legacy archive absent from the snapshot": true,
		"snapshot without a preference mutation":  false,
	} {
		t.Run(name, func(t *testing.T) {
			a := newTestApp(t)
			f := &appStateContextWA{fakeWA: newFakeWA()}
			a.wa = f
			ctx := context.Background()
			if err := a.db.UpsertChat(chat.String(), "dm", "Legacy", base); err != nil {
				t.Fatal(err)
			}
			if err := a.db.SetChatArchived(chat.String(), true); err != nil {
				t.Fatal(err)
			}
			if preferenceKnown {
				a.handleAppStatePersistenceEvent(ctx, preference, nil)
			}
			settled := base.Add(time.Hour)
			previousNowUTC := nowUTC
			nowUTC = func() time.Time { return settled }
			t.Cleanup(func() { nowUTC = previousNowUTC })
			fullReplays := 0
			f.requestAppStateRecovery = func(context.Context, string) (types.MessageID, error) {
				t.Error("verified full replay must not request a recovery snapshot")
				return "", nil
			}
			f.fetchEvents = func(_ context.Context, name string, full, _ bool) ([]any, error) {
				if name != string(appstate.WAPatchRegularLow) || !full {
					return nil, nil
				}
				fullReplays++
				// A verified snapshot with nothing for this store: whatsmeow
				// returns only the completion sentinel.
				return []any{&events.AppStateSyncComplete{Name: appstate.WAPatchRegularLow, Version: 7}}, nil
			}
			f.fetchAppState = func(ctx context.Context, name string, full, only bool) error {
				_, err := f.fetchEvents(ctx, name, full, only)
				return err
			}
			for start := 1; start <= 3; start++ {
				var recoveries sync.Map
				a.syncAppStateDeltas(ctx, &recoveries)
				if required, err := a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularLow)); err != nil || required {
					t.Fatalf("start %d: recovery intent kept after a completed full replay: %v %v", start, required, err)
				}
			}
			if fullReplays != 1 {
				t.Fatalf("full regular_low replays across 3 starts=%d, want 1", fullReplays)
			}
			got, err := a.db.GetChat(chat.String())
			if err != nil || !got.Archived {
				t.Fatalf("settled legacy archive=%+v, %v; want still archived", got, err)
			}
			var stored atomic.Int64
			message := func(id string, ts time.Time) {
				a.handleLiveSyncMessage(ctx, SyncOptions{}, &events.Message{
					Info:    types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: chat}, ID: id, Timestamp: ts},
					Message: &waE2E.Message{Conversation: proto.String("synthetic arrival")},
				}, &stored, nil, nil)
			}
			// The settlement is the chat's boundary: a message older than the
			// snapshot never unarchives it.
			message("older", settled.Add(-time.Second))
			if got, err := a.db.GetChat(chat.String()); err != nil || !got.Archived {
				t.Fatalf("message older than the settled snapshot unarchived the chat: %+v, %v", got, err)
			}
			// A newer arrival unarchives with a known preference. The settled
			// preference is unknown, not "unarchive": the arrival waits for
			// the real one, which replaces it however old its timestamp is.
			message("newer", settled.Add(time.Second))
			got, err = a.db.GetChat(chat.String())
			if err != nil || got.Archived == preferenceKnown {
				t.Fatalf("archived=%v after a message newer than the settled snapshot (preference known: %v); %v", got.Archived, preferenceKnown, err)
			}
			if !preferenceKnown {
				a.handleAppStatePersistenceEvent(ctx, preference, nil)
				if got, err := a.db.GetChat(chat.String()); err != nil || got.Archived {
					t.Fatalf("real preference did not replace the settled one: %+v, %v", got, err)
				}
			}
		})
	}
}

// Known-broken collections never see the destructive full replay, request
// one background snapshot per start, and settle once the primary's snapshot
// arrives even when it does not mention the legacy archive.
func TestAutoUnarchiveBrokenCollectionSettlesAfterRecoverySnapshot(t *testing.T) {
	a := newTestApp(t)
	f := &appStateContextWA{fakeWA: newFakeWA()}
	a.wa = f
	ctx := context.Background()
	chat := types.NewJID("15550000005", types.DefaultUserServer)
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	if err := a.db.UpsertChat(chat.String(), "dm", "Legacy", base); err != nil {
		t.Fatal(err)
	}
	if err := a.db.SetChatArchived(chat.String(), true); err != nil {
		t.Fatal(err)
	}
	a.setAppStateFullSyncResult(appstate.WAPatchRegularLow, true)
	requests := 0
	f.requestAppStateRecovery = func(context.Context, string) (types.MessageID, error) { requests++; return "snapshot", nil }
	f.fetchEvents = func(_ context.Context, name string, full, _ bool) ([]any, error) {
		if name == string(appstate.WAPatchRegularLow) && full {
			t.Error("destructive full replay on known-broken collection")
		}
		return nil, nil
	}
	f.fetchAppState = func(ctx context.Context, name string, full, only bool) error {
		_, err := f.fetchEvents(ctx, name, full, only)
		return err
	}
	var first sync.Map
	a.syncAppStateDeltas(ctx, &first)
	if requests != 1 {
		t.Fatalf("background snapshot requests=%d, want 1", requests)
	}
	if required, err := a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularLow)); err != nil || !required {
		t.Fatalf("snapshot debt cleared before the primary answered: %v, %v", required, err)
	}
	// The primary answers with a snapshot that has neither the preference nor
	// an action for the legacy archive.
	f.emit(&events.AppStateSyncComplete{Name: appstate.WAPatchRegularLow, Version: 9, Recovery: true})
	waitForCondition(t, time.Second, func() bool {
		pending, err := a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularLow))
		return err == nil && !pending
	})
	if required, err := a.db.ChatArchiveRecoveryRequired(); err != nil || required {
		t.Fatalf("archive recovery still required after the primary's snapshot: %v, %v", required, err)
	}
	got, err := a.db.GetChat(chat.String())
	if err != nil || !got.Archived {
		t.Fatalf("legacy archive after settlement=%+v, %v; want archived", got, err)
	}
	for start := 2; start <= 3; start++ {
		var next sync.Map
		a.syncAppStateDeltas(ctx, &next)
		if required, err := a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularLow)); err != nil || required {
			t.Fatalf("start %d: settled store re-armed the recovery intent: %v, %v", start, required, err)
		}
	}
	if requests != 1 {
		t.Fatalf("background snapshot requests after settlement=%d, want 1", requests)
	}
}

// archiveSettlementState reads what settlement would write: the preference row
// and the legacy chat's archive boundary.
func archiveSettlementState(t *testing.T, a *App, jid string) (preferenceRows int, actionMS int64) {
	t.Helper()
	raw, err := sql.Open("sqlite3", filepath.Join(a.opts.StoreDir, "wacli.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if err := raw.QueryRow(`SELECT COUNT(*) FROM chat_archive_setting WHERE id=1`).Scan(&preferenceRows); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRow(`SELECT archive_action_ms FROM chats WHERE jid=?`, jid).Scan(&actionMS); err != nil {
		t.Fatal(err)
	}
	return preferenceRows, actionMS
}

func failArchiveWrites(t *testing.T, a *App) (drop func()) {
	t.Helper()
	raw, err := sql.Open("sqlite3", filepath.Join(a.opts.StoreDir, "wacli.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TRIGGER fail_snapshot_archive BEFORE UPDATE OF archived ON chats
 BEGIN SELECT RAISE(FAIL, 'injected transient archive persistence failure'); END`); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	drop = func() {
		once.Do(func() {
			if _, err := raw.Exec(`DROP TRIGGER fail_snapshot_archive`); err != nil {
				t.Fatal(err)
			}
			raw.Close()
		})
	}
	t.Cleanup(drop)
	return drop
}

// A snapshot settles only when every mutation it carried reached the store.
// A legacy archived chat has no boundary; its snapshot unarchive at 12:00
// fails once with a transient write error while the completion sentinel runs
// at 12:10. Settling there would stamp a 12:10 boundary that rejects the
// 12:00 unarchive on the retried replay, leaving the chat archived forever.
func TestAutoUnarchiveFailedSnapshotMutationDefersSettlement(t *testing.T) {
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	settled := base.Add(10 * time.Minute)
	chat := types.NewJID("15550000006", types.DefaultUserServer)
	unarchive := &events.Archive{JID: chat, Timestamp: base, FromFullSync: true,
		Action: &waSyncAction.ArchiveChatAction{Archived: proto.Bool(false)}}

	setup := func(t *testing.T) (*App, *appStateContextWA) {
		t.Helper()
		a := newTestApp(t)
		f := &appStateContextWA{fakeWA: newFakeWA()}
		a.wa = f
		if err := a.db.UpsertChat(chat.String(), "dm", "Legacy", base.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
		if err := a.db.SetChatArchived(chat.String(), true); err != nil {
			t.Fatal(err)
		}
		previousNowUTC := nowUTC
		nowUTC = func() time.Time { return settled }
		t.Cleanup(func() { nowUTC = previousNowUTC })
		f.fetchAppState = func(context.Context, string, bool, bool) error { return nil }
		return a, f
	}
	assertUnsettled := func(t *testing.T, a *App, stage string) {
		t.Helper()
		if required, err := a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularLow)); err != nil || !required {
			t.Fatalf("%s: replay debt cleared after a failed snapshot write: %v, %v", stage, required, err)
		}
		if required, err := a.db.ChatArchiveRecoveryRequired(); err != nil || !required {
			t.Fatalf("%s: archive recovery settled after a failed snapshot write: %v, %v", stage, required, err)
		}
		rows, actionMS := archiveSettlementState(t, a, chat.String())
		if rows != 0 || actionMS != 0 {
			t.Fatalf("%s: failed snapshot settled anyway: preference rows=%d archive_action_ms=%d", stage, rows, actionMS)
		}
	}
	assertApplied := func(t *testing.T, a *App, stage string) {
		t.Helper()
		got, err := a.db.GetChat(chat.String())
		if err != nil || got.Archived {
			t.Fatalf("%s: retried snapshot did not apply the original unarchive: %+v, %v", stage, got, err)
		}
		rows, actionMS := archiveSettlementState(t, a, chat.String())
		if rows != 1 || actionMS != base.UnixMilli() {
			t.Fatalf("%s: settlement after the retried snapshot: preference rows=%d archive_action_ms=%d, want 1 and %d", stage, rows, actionMS, base.UnixMilli())
		}
		if required, err := a.db.ChatArchiveRecoveryRequired(); err != nil || required {
			t.Fatalf("%s: archive recovery still required after the retried snapshot: %v, %v", stage, required, err)
		}
		if required, err := a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularLow)); err != nil || required {
			t.Fatalf("%s: replay debt kept after the retried snapshot: %v, %v", stage, required, err)
		}
	}

	t.Run("verified full replay", func(t *testing.T) {
		a, f := setup(t)
		ctx := context.Background()
		drop := failArchiveWrites(t, a)
		f.requestAppStateRecovery = func(context.Context, string) (types.MessageID, error) {
			t.Error("local persistence failure must not request a phone snapshot")
			return "", nil
		}
		fullReplays := 0
		f.fetchEvents = func(_ context.Context, name string, full, _ bool) ([]any, error) {
			if name != string(appstate.WAPatchRegularLow) || !full {
				return nil, nil
			}
			fullReplays++
			return []any{unarchive, &events.AppStateSyncComplete{Name: appstate.WAPatchRegularLow, Version: 7}}, nil
		}
		var first sync.Map
		a.syncAppStateDeltas(ctx, &first)
		if fullReplays != 1 {
			t.Fatalf("full replays after the failing start=%d, want 1", fullReplays)
		}
		assertUnsettled(t, a, "failed replay")
		if got, err := a.db.GetChat(chat.String()); err != nil || !got.Archived {
			t.Fatalf("failed write changed the archived state: %+v, %v", got, err)
		}
		drop()
		var second sync.Map
		a.syncAppStateDeltas(ctx, &second)
		if fullReplays != 2 {
			t.Fatalf("full replays after the retry start=%d, want 2", fullReplays)
		}
		assertApplied(t, a, "retried replay")
		var third sync.Map
		a.syncAppStateDeltas(ctx, &third)
		if fullReplays != 2 {
			t.Fatalf("settled store replayed again: full replays=%d, want 2", fullReplays)
		}
	})

	t.Run("phone recovery answer", func(t *testing.T) {
		a, f := setup(t)
		ctx := context.Background()
		a.setAppStateFullSyncResult(appstate.WAPatchRegularLow, true)
		drop := failArchiveWrites(t, a)
		requests := 0
		f.requestAppStateRecovery = func(context.Context, string) (types.MessageID, error) { requests++; return "snapshot", nil }
		f.fetchEvents = func(_ context.Context, name string, full, _ bool) ([]any, error) {
			if name == string(appstate.WAPatchRegularLow) && full {
				t.Error("destructive full replay on known-broken collection")
			}
			return nil, nil
		}
		answer := func() {
			f.emit(unarchive)
			f.emit(&events.AppStateSyncComplete{Name: appstate.WAPatchRegularLow, Version: 9, Recovery: true})
			if err := a.appStatePersist.waitIdle(ctx); err != nil {
				t.Fatal(err)
			}
		}
		// Each start is a new process: the in-memory request spacing resets.
		start := func(recoveries *sync.Map) {
			a.appStateDegradedMu.Lock()
			a.appStateRecoveryRequested = nil
			a.appStateDegradedMu.Unlock()
			a.syncAppStateDeltas(ctx, recoveries)
		}
		var first sync.Map
		start(&first)
		if requests != 1 {
			t.Fatalf("snapshot requests after the first start=%d, want 1", requests)
		}
		answer()
		assertUnsettled(t, a, "failed recovery answer")
		if got, err := a.db.GetChat(chat.String()); err != nil || !got.Archived {
			t.Fatalf("failed write changed the archived state: %+v, %v", got, err)
		}
		drop()
		var second sync.Map
		start(&second)
		if requests != 2 {
			t.Fatalf("kept debt did not request the snapshot again: requests=%d, want 2", requests)
		}
		answer()
		assertApplied(t, a, "retried recovery answer")
		var third sync.Map
		start(&third)
		if requests != 2 {
			t.Fatalf("settled store requested a snapshot again: requests=%d, want 2", requests)
		}
	})
}

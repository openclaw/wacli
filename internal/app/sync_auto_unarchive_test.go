package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/out"
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
			// Keep this test on the primary-snapshot path, not the archive
			// fallback after a successful incremental fetch.
			return nil, errors.New("incremental unavailable")
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
	// The archive metadata is the durable debt; it records no replay intent.
	if required, err := a.db.ChatArchiveRecoveryRequired(); err != nil || !required {
		t.Fatalf("incremental incorrectly settled the archive recovery: %v, %v", required, err)
	}
	if required, err := a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularLow)); err != nil || required {
		t.Fatalf("archive recovery recorded a replay intent: %v, %v", required, err)
	}
	f.emit(&events.UnarchiveChatsSetting{Timestamp: base, Action: &waSyncAction.UnarchiveChatsSetting{UnarchiveChats: proto.Bool(true)}, FromFullSync: true})
	f.emit(&events.Archive{JID: chat, Timestamp: base.Add(time.Second), Action: &waSyncAction.ArchiveChatAction{Archived: proto.Bool(true)}, FromFullSync: true})
	f.emit(&events.AppStateSyncComplete{Name: appstate.WAPatchRegularLow, Recovery: true})
	waitForCondition(t, time.Second, func() bool {
		pending, err := a.db.ChatArchiveRecoveryRequired()
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
// request the full replay again on every start. The archive metadata alone no
// longer runs a full replay; the one here is owed to replay debt the store
// already carried (an interrupted write), and its verified snapshot settles
// the archive recovery as well.
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
			if err := a.db.MarkAppStateRecoveryRequired(string(appstate.WAPatchRegularLow)); err != nil {
				t.Fatal(err)
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
		if name == string(appstate.WAPatchRegularLow) {
			// Keep this test on the primary-snapshot path, not the settlement
			// after a successful incremental fetch (covered below).
			return nil, errors.New("incremental unavailable")
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
	// The archive metadata is the durable debt; it records no replay intent.
	if required, err := a.db.ChatArchiveRecoveryRequired(); err != nil || !required {
		t.Fatalf("archive recovery settled before the primary answered: %v, %v", required, err)
	}
	if required, err := a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularLow)); err != nil || required {
		t.Fatalf("archive recovery recorded a replay intent: %v, %v", required, err)
	}
	// The primary answers with a snapshot that has neither the preference nor
	// an action for the legacy archive.
	f.emit(&events.AppStateSyncComplete{Name: appstate.WAPatchRegularLow, Version: 9, Recovery: true})
	waitForCondition(t, time.Second, func() bool {
		required, err := a.db.ChatArchiveRecoveryRequired()
		return err == nil && !required
	})
	got, err := a.db.GetChat(chat.String())
	if err != nil || !got.Archived {
		t.Fatalf("legacy archive after settlement=%+v, %v; want archived", got, err)
	}
	for start := 2; start <= 3; start++ {
		var next sync.Map
		a.syncAppStateDeltas(ctx, &next)
		if required, err := a.db.ChatArchiveRecoveryRequired(); err != nil || required {
			t.Fatalf("start %d: settled store re-armed the archive recovery: %v, %v", start, required, err)
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

func TestAutoUnarchiveBrokenCollectionSettlesWithoutSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name            string
		broken          bool
		noDebt          bool
		fetchFail       bool
		persistenceFail bool
		wantSettled     bool
	}{
		{name: "broken successful incremental", broken: true, wantSettled: true},
		{name: "not broken"},
		{name: "failed incremental", broken: true, fetchFail: true},
		{name: "failed persistence", broken: true, persistenceFail: true},
		// Archive metadata only: the phone is asked once and may never answer.
		{name: "broken without replay debt", broken: true, noDebt: true, wantSettled: true},
		{name: "broken without replay debt, failed incremental", broken: true, noDebt: true, fetchFail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestApp(t)
			warnings := captureWarningCodes(t, a)
			f := &appStateContextWA{fakeWA: newFakeWA()}
			a.wa = f
			chat := types.NewJID("15550000007", types.DefaultUserServer)
			base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
			settled := base.Add(time.Hour)
			previousNowUTC := nowUTC
			nowUTC = func() time.Time { return settled }
			t.Cleanup(func() { nowUTC = previousNowUTC })
			if err := a.db.UpsertChat(chat.String(), "dm", "Legacy", base); err != nil {
				t.Fatal(err)
			}
			if err := a.db.SetChatArchived(chat.String(), true); err != nil {
				t.Fatal(err)
			}
			// Existing replay debt must survive the archive-only settlement.
			if !tc.noDebt {
				if _, _, err := a.db.BeginAppStateRecovery(string(appstate.WAPatchRegularLow)); err != nil {
					t.Fatal(err)
				}
			}
			a.setAppStateFullSyncResult(appstate.WAPatchRegularLow, tc.broken)
			if tc.persistenceFail {
				failArchiveWrites(t, a)
			}
			requests, fullReplays := 0, 0
			f.requestAppStateRecovery = func(context.Context, string) (types.MessageID, error) {
				requests++
				return "snapshot", nil
			}
			f.fetchEvents = func(_ context.Context, name string, full, _ bool) ([]any, error) {
				if name != string(appstate.WAPatchRegularLow) {
					return nil, nil
				}
				if full {
					fullReplays++
				}
				if tc.fetchFail {
					return nil, errors.New("injected incremental failure")
				}
				if tc.persistenceFail {
					return []any{&events.Archive{JID: chat, Timestamp: base,
						Action: &waSyncAction.ArchiveChatAction{Archived: proto.Bool(false)}}}, nil
				}
				return nil, nil
			}
			f.fetchAppState = func(context.Context, string, bool, bool) error { return nil }
			raw, err := sql.Open("sqlite3", filepath.Join(a.opts.StoreDir, "wacli.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			var recoveries sync.Map
			for cycle := 1; cycle <= 3; cycle++ {
				a.syncAppStateDeltas(t.Context(), &recoveries)
				if required, err := a.db.ChatArchiveRecoveryRequired(); err != nil || required == tc.wantSettled {
					t.Fatalf("cycle %d: archive recovery required=%v, err=%v", cycle, required, err)
				}
				var intents int
				if err := raw.QueryRow(`SELECT COUNT(*) FROM app_state_recovery_intents WHERE collection='regular_low'`).Scan(&intents); err != nil {
					t.Fatal(err)
				}
				// The archive metadata is recovered from the phone and records no
				// replay intent; a broken collection keeps its debt plus the
				// snapshot request's own intent, a healthy one replays it away.
				wantIntents := 0
				if tc.broken && !tc.noDebt {
					wantIntents = 2
				}
				if intents != wantIntents {
					t.Fatalf("cycle %d: replay intents=%d, want %d", cycle, intents, wantIntents)
				}
			}
			rows, boundary := archiveSettlementState(t, a, chat.String())
			if tc.wantSettled {
				var id, enabled int
				var timestamp int64
				if err := raw.QueryRow(`SELECT id,enabled,timestamp_ms FROM chat_archive_setting`).Scan(&id, &enabled, &timestamp); err != nil {
					t.Fatal(err)
				}
				if id != 1 || enabled != 0 || timestamp != 0 || rows != 1 || boundary != settled.UnixMilli() {
					t.Fatalf("setting=(%d,%d,%d), rows=%d, boundary=%d", id, enabled, timestamp, rows, boundary)
				}
			} else if rows != 0 || boundary != 0 {
				t.Fatalf("unexpected settlement: rows=%d, boundary=%d", rows, boundary)
			}
			if got, err := a.db.GetChat(chat.String()); err != nil || !got.Archived {
				t.Fatalf("legacy archive changed: %+v, %v", got, err)
			}
			settlementWarnings := 0
			for _, code := range warnings() {
				if code == "app_state_archive_settled_without_snapshot" {
					settlementWarnings++
				}
			}
			wantWarnings := 0
			if tc.wantSettled {
				wantWarnings = 1
			}
			if settlementWarnings != wantWarnings {
				t.Fatalf("settlement warnings=%d, want %d", settlementWarnings, wantWarnings)
			}
			if tc.broken && (requests != 1 || fullReplays != 0) {
				t.Fatalf("broken collection: requests=%d, full replays=%d", requests, fullReplays)
			}
			// Healthy: the debt is replayed once, then the archive metadata is
			// asked from the phone once per process.
			if !tc.broken && (requests != 1 || fullReplays != 1) {
				t.Fatalf("healthy collection: requests=%d, full replays=%d", requests, fullReplays)
			}
		})
	}
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
// The full replay subtest owes its replay to debt the store already carries;
// the phone subtest is the archive metadata's own request, which records no
// intent.
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
	assertUnsettled := func(t *testing.T, a *App, stage string, replayDebt bool) {
		t.Helper()
		if required, err := a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularLow)); err != nil || required != replayDebt {
			t.Fatalf("%s: replay debt after a failed snapshot write=%v, want %v; %v", stage, required, replayDebt, err)
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
		if err := a.db.MarkAppStateRecoveryRequired(string(appstate.WAPatchRegularLow)); err != nil {
			t.Fatal(err)
		}
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
		assertUnsettled(t, a, "failed replay", true)
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
			// Exercise snapshot persistence failures without first settling
			// through a successful incremental fetch.
			if name == string(appstate.WAPatchRegularLow) {
				return nil, errors.New("incremental unavailable")
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
		assertUnsettled(t, a, "failed recovery answer", false)
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

// The first start on an upgraded store must not reach for a full regular_low
// replay. With stock whatsmeow a full sync deletes the saved collection version
// before decoding, so a server snapshot that fails verification leaves the
// device without usable local app state until the primary answers. The archive
// metadata older builds did not save is recovered from the primary's snapshot
// instead, requested in the background while the collection keeps syncing
// incrementally, and no replay intent is recorded for it. Stock whatsmeow
// ignores a recovery answer whose version is not newer than the local one
// (handleAppStateRecovery: currentVersion >= version), so an answer at the
// local version settles nothing but also loses nothing; a strictly newer one
// settles the recovery through the ordered persistence path.
func TestAutoUnarchiveUpgradeRecoveryPrefersPhoneSnapshot(t *testing.T) {
	a := newTestApp(t)
	f := &appStateContextWA{fakeWA: newFakeWA()}
	a.wa = f
	ctx := context.Background()
	chat := types.NewJID("15550000007", types.DefaultUserServer)
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	if err := a.db.UpsertChat(chat.String(), "dm", "Legacy", base); err != nil {
		t.Fatal(err)
	}
	if err := a.db.SetChatArchived(chat.String(), true); err != nil {
		t.Fatal(err)
	}
	// A healthy upgraded store: the SDK holds verified local state for
	// regular_low at localVersion and the collection is not known-broken.
	const localVersion uint64 = 528
	f.hasLocalAppState = true
	fullReplays, incremental, requests := 0, 0, 0
	f.requestAppStateRecovery = func(context.Context, string) (types.MessageID, error) { requests++; return "snapshot", nil }
	f.fetchEvents = func(_ context.Context, name string, full, _ bool) ([]any, error) {
		if name != string(appstate.WAPatchRegularLow) {
			return nil, nil
		}
		if !full {
			incremental++
			return nil, nil
		}
		// The server's snapshot does not verify. The fake mirrors stock
		// whatsmeow: the saved collection version is already gone.
		fullReplays++
		return nil, appstate.ErrMismatchingLTHash
	}
	f.fetchAppState = func(ctx context.Context, name string, full, only bool) error {
		_, err := f.fetchEvents(ctx, name, full, only)
		return err
	}
	// answer hands the primary's recovery response to the app the way stock
	// whatsmeow does: dropped at or below the local version, otherwise the
	// snapshot's mutations followed by the Recovery completion sentinel.
	answer := func(version uint64, mutations ...any) {
		t.Helper()
		if version <= localVersion {
			return
		}
		for _, mutation := range mutations {
			f.emit(mutation)
		}
		f.emit(&events.AppStateSyncComplete{Name: appstate.WAPatchRegularLow, Version: version, Recovery: true})
		if err := a.appStatePersist.waitIdle(ctx); err != nil {
			t.Fatal(err)
		}
	}
	// Each start is a new process: the in-memory request spacing resets.
	start := func() {
		a.appStateDegradedMu.Lock()
		a.appStateRecoveryRequested = nil
		a.appStateDegradedMu.Unlock()
		var recoveries sync.Map
		a.syncAppStateDeltas(ctx, &recoveries)
	}
	assertIntact := func(stage string) {
		t.Helper()
		if fullReplays != 0 {
			t.Fatalf("%s: full regular_low replays on an upgraded store=%d, want 0", stage, fullReplays)
		}
		if !f.hasLocalAppState {
			t.Fatalf("%s: local regular_low state was destroyed", stage)
		}
		if required, err := a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularLow)); err != nil || required {
			t.Fatalf("%s: archive recovery recorded a replay intent: %v, %v", stage, required, err)
		}
		if required, err := a.db.ChatArchiveRecoveryRequired(); err != nil || !required {
			t.Fatalf("%s: archive recovery settled without a snapshot: %v, %v", stage, required, err)
		}
		rows, actionMS := archiveSettlementState(t, a, chat.String())
		if rows != 0 || actionMS != 0 {
			t.Fatalf("%s: settled without a snapshot: preference rows=%d archive_action_ms=%d", stage, rows, actionMS)
		}
		if got, err := a.db.GetChat(chat.String()); err != nil || !got.Archived {
			t.Fatalf("%s: legacy archive changed without a snapshot: %+v, %v", stage, got, err)
		}
	}

	start()
	assertIntact("first start")
	if requests != 1 {
		t.Fatalf("background snapshot requests after the first start=%d, want 1", requests)
	}
	if incremental != 1 {
		t.Fatalf("incremental regular_low fetches after the first start=%d, want 1", incremental)
	}

	// The primary answers at the local version: stock whatsmeow drops it.
	answer(localVersion)
	assertIntact("equal-version answer")
	start()
	if requests != 2 {
		t.Fatalf("unsettled store did not request the snapshot again: requests=%d, want 2", requests)
	}
	assertIntact("second start")

	// A strictly newer snapshot is adopted and settles the recovery.
	answer(localVersion+1,
		&events.UnarchiveChatsSetting{Timestamp: base, FromFullSync: true,
			Action: &waSyncAction.UnarchiveChatsSetting{UnarchiveChats: proto.Bool(true)}},
		&events.Archive{JID: chat, Timestamp: base.Add(time.Second), FromFullSync: true,
			Action: &waSyncAction.ArchiveChatAction{Archived: proto.Bool(true)}})
	waitForCondition(t, time.Second, func() bool {
		required, err := a.db.ChatArchiveRecoveryRequired()
		return err == nil && !required
	})
	rows, actionMS := archiveSettlementState(t, a, chat.String())
	if rows != 1 || actionMS != base.Add(time.Second).UnixMilli() {
		t.Fatalf("settlement after the newer snapshot: preference rows=%d archive_action_ms=%d, want 1 and %d", rows, actionMS, base.Add(time.Second).UnixMilli())
	}
	if got, err := a.db.GetChat(chat.String()); err != nil || !got.Archived {
		t.Fatalf("snapshot archive after settlement=%+v, %v; want archived", got, err)
	}
	start()
	if requests != 2 || fullReplays != 0 {
		t.Fatalf("settled store requested again: requests=%d full replays=%d, want 2 and 0", requests, fullReplays)
	}
	if required, err := a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularLow)); err != nil || required {
		t.Fatalf("settled store carries a replay intent: %v, %v", required, err)
	}
}

// warningCodes collects the codes of emitted warning events; the writer may be
// called from the late recovery goroutine while the test reads it.
type warningCodes struct {
	mu    sync.Mutex
	codes []string
}

func (w *warningCodes) Write(p []byte) (int, error) {
	var event struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(p, &event); err != nil {
		return 0, err
	}
	if code, ok := event.Data["code"].(string); ok {
		w.mu.Lock()
		w.codes = append(w.codes, code)
		w.mu.Unlock()
	}
	return len(p), nil
}

func (w *warningCodes) has(code string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Contains(w.codes, code)
}

// The archive metadata's snapshot request owns no replay intent. A write
// recorded while the primary is asleep keeps its own intent, and the settled
// snapshot is reported as completed, not as a failed recovery.
func TestAutoUnarchivePhoneSnapshotLeavesLaterReplayDebt(t *testing.T) {
	a := newTestApp(t)
	f := &appStateContextWA{fakeWA: newFakeWA()}
	a.wa = f
	codes := &warningCodes{}
	a.opts.Events = out.NewEventWriter(codes, true)
	ctx := context.Background()
	chat := types.NewJID("15550000008", types.DefaultUserServer)
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	if err := a.db.UpsertChat(chat.String(), "dm", "Legacy", base); err != nil {
		t.Fatal(err)
	}
	if err := a.db.SetChatArchived(chat.String(), true); err != nil {
		t.Fatal(err)
	}
	f.requestAppStateRecovery = func(context.Context, string) (types.MessageID, error) { return "snapshot", nil }
	f.fetchAppState = func(context.Context, string, bool, bool) error { return nil }
	var recoveries sync.Map
	a.syncAppStateDeltas(ctx, &recoveries)
	// A chat-state write while the snapshot is pending keeps its intent durable.
	if err := a.db.MarkAppStateRecoveryRequired(string(appstate.WAPatchRegularLow)); err != nil {
		t.Fatal(err)
	}
	f.emit(&events.AppStateSyncComplete{Name: appstate.WAPatchRegularLow, Version: 9, Recovery: true})
	waitForCondition(t, time.Second, func() bool { return codes.has("app_state_recovery_completed_late") })
	if required, err := a.db.ChatArchiveRecoveryRequired(); err != nil || required {
		t.Fatalf("archive recovery not settled by the snapshot: %v, %v", required, err)
	}
	if required, err := a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularLow)); err != nil || !required {
		t.Fatalf("snapshot cleared replay debt recorded after its request: %v, %v", required, err)
	}
	if codes.has("app_state_recovery_failed") {
		t.Fatalf("settled snapshot reported as a failed recovery: %v", codes.codes)
	}
}

package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func testStickerDigest(name string) []byte {
	digest := sha256.Sum256([]byte(name))
	return digest[:]
}

func favoriteStickerAppState(name string, favorite bool, at time.Time) *events.AppState {
	return &events.AppState{
		Index: []string{appstate.IndexFavoriteSticker, base64.StdEncoding.EncodeToString(testStickerDigest(name))},
		SyncActionValue: &waSyncAction.SyncActionValue{
			Timestamp: proto.Int64(at.UnixMilli()),
			StickerAction: &waSyncAction.StickerAction{
				DirectPath:    proto.String("/v/t62/" + name),
				MediaKey:      make([]byte, 32),
				FileEncSHA256: make([]byte, 32),
				Mimetype:      proto.String("image/webp"),
				IsFavorite:    proto.Bool(favorite),
			},
		},
	}
}

func favoriteStickerHashes(t *testing.T, a *App) []string {
	t.Helper()
	stickers, err := a.db.ListStickers(store.ListStickersParams{Favorites: true})
	if err != nil {
		t.Fatal(err)
	}
	var hashes []string
	for _, s := range stickers {
		hashes = append(hashes, s.FileHash)
	}
	return hashes
}

func TestLiveFavoriteStickerEventsPersistThroughAppStateQueue(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	at := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	added := favoriteStickerAppState("cat", true, at)
	if got := appStateCollectionsForEvent(added); !slices.Equal(got, []appstate.WAPatchName{appstate.WAPatchRegularLow}) {
		t.Fatalf("favorite sticker recovery collections = %v, want regular_low", got)
	}
	for _, evt := range []*events.AppState{added, favoriteStickerAppState("dog", true, at), favoriteStickerAppState("dog", false, at.Add(time.Minute))} {
		persisted := make(chan error, 1)
		a.handleAppStatePersistenceEvent(context.Background(), evt, func(err error) { persisted <- err })
		select {
		case err := <-persisted:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("favorite sticker event was not persisted")
		}
	}
	want := base64.StdEncoding.EncodeToString(testStickerDigest("cat"))
	if got := favoriteStickerHashes(t, a); !slices.Equal(got, []string{want}) {
		t.Fatalf("favorites = %v, want [%s]", got, want)
	}
	if required, err := a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularLow)); err != nil || required {
		t.Fatalf("live persistence left regular_low replay debt: %v, %v", required, err)
	}
}

func TestRefreshFavoriteStickersReplaysRegularLowInFull(t *testing.T) {
	a := newTestApp(t)
	f := &appStateContextWA{fakeWA: newFakeWA()}
	var fetches []string
	f.fetchEvents = func(_ context.Context, name string, fullSync, _ bool) ([]any, error) {
		fetches = append(fetches, fmt.Sprintf("%s full=%v", name, fullSync))
		return []any{favoriteStickerAppState("cat", true, time.Now())}, nil
	}
	f.requestAppStateRecovery = func(context.Context, string) (types.MessageID, error) {
		t.Error("a verified full replay must not ask the phone for a snapshot")
		return "", nil
	}
	a.wa = f

	if err := a.RefreshFavoriteStickers(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fetches, []string{"regular_low full=true"}) {
		t.Fatalf("fetches = %v, want one full regular_low replay", fetches)
	}
	if got := favoriteStickerHashes(t, a); len(got) != 1 {
		t.Fatalf("favorites after refresh = %v", got)
	}
	if required, err := a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularLow)); err != nil || required {
		t.Fatalf("completed refresh left replay debt: %v, %v", required, err)
	}
}

func TestSyncRefreshStickersReplaysRegularLowAtStart(t *testing.T) {
	a := newTestApp(t)
	f := &appStateContextWA{fakeWA: newFakeWA()}
	var fullFetches []string
	f.fetchEvents = func(_ context.Context, name string, fullSync, _ bool) ([]any, error) {
		if fullSync {
			fullFetches = append(fullFetches, name)
		}
		return []any{favoriteStickerAppState("cat", true, time.Now())}, nil
	}
	f.fetchAppState = func(context.Context, string, bool, bool) error { return nil }
	f.requestAppStateRecovery = func(context.Context, string) (types.MessageID, error) {
		t.Error("a verified full replay must not ask the phone for a snapshot")
		return "", nil
	}
	a.wa = f
	// Without a stored archive setting, sync start replays regular_low itself.
	if err := a.db.SetChatArchiveSetting(true, time.Now()); err != nil {
		t.Fatal(err)
	}

	if _, err := a.Sync(t.Context(), SyncOptions{Mode: SyncModeOnce, IdleExit: time.Millisecond, RefreshStickers: true}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fullFetches, []string{"regular_low"}) {
		t.Fatalf("full fetches = %v, want one regular_low replay", fullFetches)
	}
	if got := favoriteStickerHashes(t, a); len(got) != 1 {
		t.Fatalf("favorites after sync --refresh-stickers = %v", got)
	}
}

func TestFirstSyncAfterUpgradeBackfillsFavoriteStickers(t *testing.T) {
	a := newTestApp(t)
	f := &appStateContextWA{fakeWA: newFakeWA()}
	f.fetchEvents = func(_ context.Context, name string, fullSync, _ bool) ([]any, error) {
		if fullSync && name == string(appstate.WAPatchRegularLow) {
			return []any{favoriteStickerAppState("cat", true, time.Now())}, nil
		}
		return nil, nil
	}
	f.fetchAppState = func(context.Context, string, bool, bool) error { return nil }
	f.requestAppStateRecovery = func(context.Context, string) (types.MessageID, error) {
		t.Error("a verified full replay must not ask the phone for a snapshot")
		return "", nil
	}
	a.wa = f

	if _, err := a.Sync(t.Context(), SyncOptions{Mode: SyncModeOnce, IdleExit: time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	if got := favoriteStickerHashes(t, a); len(got) != 1 {
		t.Fatalf("favorites after the first sync of an upgraded store = %v, want the replayed favorite", got)
	}
}

func TestRefreshFavoriteStickersWaitsForPhoneSnapshotOnBrokenCollection(t *testing.T) {
	a, f := degradedTestApp(t, types.JID{User: "15550000030", Server: types.DefaultUserServer})
	name := appstate.WAPatchRegularLow
	var fullSyncs atomic.Int32
	f.fetchEvents = func(_ context.Context, _ string, fullSync, _ bool) ([]any, error) {
		if fullSync {
			fullSyncs.Add(1)
		}
		return nil, nil
	}
	forceBrokenFullSync(t, a, f, name)
	f.hasLocalAppState = true
	f.requestAppStateRecovery = func(context.Context, string) (types.MessageID, error) {
		go func() {
			f.emit(favoriteStickerAppState("cat", true, time.Now()))
			f.emit(&events.AppStateSyncComplete{Name: name, Recovery: true})
		}()
		return "req", nil
	}

	var eventLog bytes.Buffer
	a.opts.Events = out.NewEventWriter(&eventLog, true)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := a.RefreshFavoriteStickers(ctx); err != nil {
		t.Fatal(err)
	}
	if got := fullSyncs.Load(); got != 0 {
		t.Fatalf("full syncs on a known-broken collection = %d, want 0", got)
	}
	if !strings.Contains(eventLog.String(), `"event":"app_state_recovery_requested"`) {
		t.Fatalf("events = %q, want the phone request reported while the refresh waits", eventLog.String())
	}
	if got := favoriteStickerHashes(t, a); len(got) != 1 {
		t.Fatalf("favorites after the phone snapshot = %v, want the snapshot's favorite", got)
	}
	if required, err := a.db.AppStateRecoveryRequired(string(name)); err != nil || required {
		t.Fatalf("completed refresh left replay debt: %v, %v", required, err)
	}
}

func TestRefreshFavoriteStickersFailsWhileThePhoneSnapshotIsPending(t *testing.T) {
	a, f := degradedTestApp(t, types.JID{User: "15550000031", Server: types.DefaultUserServer})
	name := appstate.WAPatchRegularLow
	f.fetchEvents = func(context.Context, string, bool, bool) ([]any, error) { return nil, nil }
	forceBrokenFullSync(t, a, f, name)
	f.hasLocalAppState = true
	f.requestAppStateRecovery = func(context.Context, string) (types.MessageID, error) { return "req", nil }

	ctx, cancel := context.WithTimeout(t.Context(), 400*time.Millisecond)
	defer cancel()
	if err := a.RefreshFavoriteStickers(ctx); err == nil {
		t.Fatal("refresh reported success before the phone snapshot arrived")
	}
	if required, err := a.db.AppStateRecoveryRequired(string(name)); err != nil || !required {
		t.Fatalf("pending refresh intent = %v, %v; want kept for the next sync", required, err)
	}
}

func TestFailedFavoriteStickerRefreshKeepsDurableReplayIntent(t *testing.T) {
	a := newTestApp(t)
	f := &appStateContextWA{fakeWA: newFakeWA()}
	f.fetchEvents = func(context.Context, string, bool, bool) ([]any, error) {
		return nil, errors.New("server unavailable")
	}
	a.wa = f

	if err := a.RefreshFavoriteStickers(t.Context()); err == nil {
		t.Fatal("failed replay reported success")
	}
	// The next sync start drains this intent with another full replay.
	if required, err := a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularLow)); err != nil || !required {
		t.Fatalf("failed refresh intent = %v, %v; want it kept", required, err)
	}
}

func TestHistorySyncStoresFavoriteAndRecentStickers(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	sentAt := time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	history := &events.HistorySync{Data: &waHistorySync.HistorySync{
		SyncType: waHistorySync.HistorySync_INITIAL_BOOTSTRAP.Enum(),
		FavoriteStickers: []*waHistorySync.StickerMetadata{{
			FileSHA256: testStickerDigest("favorite"), DirectPath: proto.String("/v/t62/favorite"),
			MediaKey: make([]byte, 32), FileEncSHA256: make([]byte, 32),
		}},
		RecentStickers: []*waHistorySync.StickerMetadata{
			{FileSHA256: testStickerDigest("recent"), Weight: proto.Float32(1.5), LastStickerSentTS: proto.Int64(sentAt.UnixMilli())},
			{FileSHA256: []byte("not a digest")},
		},
	}}
	var stored, lastEvent atomic.Int64
	a.handleHistorySync(context.Background(), SyncOptions{}, history, &stored, &lastEvent, func(string, string) {})

	stickers, err := a.db.ListStickers(store.ListStickersParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(stickers) != 2 {
		t.Fatalf("stickers = %+v, want one favorite and one recent", stickers)
	}
	favorite, recent := stickers[0], stickers[1]
	if !favorite.Favorite || favorite.FileHash != base64.StdEncoding.EncodeToString(testStickerDigest("favorite")) || !favorite.Downloadable {
		t.Fatalf("favorite = %+v", favorite)
	}
	if !recent.Recent || recent.Favorite || recent.RecentWeight != 1.5 || recent.LastSentAt == nil || !recent.LastSentAt.Equal(sentAt) {
		t.Fatalf("recent = %+v", recent)
	}
}

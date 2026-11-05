package wa_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waServerSync"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	waStore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// The contact mirror end to end with the pinned SDK: the real whatsmeow
// client over a disposable sqlstore with the journal installed, the real
// App persistence handler, and wacli.db. Mutations go through
// dispatchAppState exactly as a fetched batch does (store write now, event
// dispatched whenever); only the network is absent.
func TestSDKContactMutationsAreMirroredInApplicationOrder(t *testing.T) {
	dir := t.TempDir()
	wa.PairedSessionStoreAt(t, filepath.Join(dir, "session.db"))
	a, err := app.New(app.Options{StoreDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	if err := a.OpenWA(); err != nil {
		t.Fatal(err)
	}
	cli := wa.RawClient(a.WA().(*wa.Client))
	ctx := t.Context()
	pn := types.NewJID("15550002222", types.DefaultUserServer)
	lid := types.NewJID("222222222222222", types.HiddenUserServer)
	if err := cli.Store.LIDs.PutLIDMapping(ctx, lid, pn); err != nil {
		t.Fatal(err)
	}
	remove, err := a.AddChatStatePersistenceHandler(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(remove)
	apply := func(ctx context.Context, jid types.JID, full, first string) *events.Contact {
		t.Helper()
		evt := cli.DangerousInternals().DispatchAppState(ctx, appstate.WAPatchCriticalUnblockLow, appstate.Mutation{
			Operation: waServerSync.SyncdMutation_SET,
			Index:     []string{appstate.IndexContact, jid.String()},
			Action:    &waSyncAction.SyncActionValue{ContactAction: &waSyncAction.ContactAction{FullName: proto.String(full), FirstName: proto.String(first)}},
		}, false)
		contact, ok := evt.(*events.Contact)
		if !ok {
			t.Fatalf("dispatchAppState returned %T", evt)
		}
		return contact
	}
	mirror := func(wantFull, wantFirst string) {
		t.Helper()
		var full, first sql.NullString
		deadline := time.Now().Add(5 * time.Second)
		for {
			raw, err := sql.Open("sqlite3", filepath.Join(dir, "wacli.db"))
			if err != nil {
				t.Fatal(err)
			}
			err = raw.QueryRow(`SELECT full_name, first_name FROM contacts WHERE jid = ?`, pn.String()).Scan(&full, &first)
			raw.Close()
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				t.Fatal(err)
			}
			if err == nil && full.String == wantFull && first.String == wantFirst {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("wacli.db row for %s = %q/%q (found %v), want %q/%q", pn, full.String, first.String, err == nil, wantFull, wantFirst)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	// 1. A batch touching both indices of one contact, events dispatched in
	//    the opposite order: the mirror ends on the last-applied name.
	echo := apply(ctx, pn, "Ada Lovelace", "Ada")
	rename := apply(ctx, lid, "Grace Hopper", "Grace")
	cli.DangerousInternals().DispatchEvent(rename)
	cli.DangerousInternals().DispatchEvent(echo)
	mirror("Grace Hopper", "Grace")

	// 2. The store write fails (cancelled context) but the SDK still emits
	//    the event and advanced the cursor: the intended names are mirrored.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	failed := apply(cancelled, pn, "Rear Admiral Hopper", "Rear")
	if stored, _ := cli.Store.Contacts.GetContact(ctx, pn); stored.FullName != "Ada Lovelace" {
		t.Fatalf("test setup: the cancelled store write went through: %+v", stored)
	}
	cli.DangerousInternals().DispatchEvent(failed)
	mirror("Rear Admiral Hopper", "Rear")

	// 3. A nameless action clears the mirror.
	cli.DangerousInternals().DispatchEvent(apply(ctx, lid, "", ""))
	mirror("", "")
}

type pausingLIDStore struct {
	waStore.LIDStore
	pause   atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (s *pausingLIDStore) GetPNForLID(ctx context.Context, lid types.JID) (types.JID, error) {
	if s.pause.Swap(false) {
		close(s.entered)
		<-s.release
	}
	return s.LIDStore.GetPNForLID(ctx, lid)
}

// The marker-failure fallback of the persistence handler drains the journal
// outside the ticket order, from each SDK dispatch goroutine. With the pinned
// SDK, the real handler and both SQLite databases: a drain that read the
// older journal snapshot and paused while canonicalizing its LID must not
// commit after a concurrent drain of the newer write. Only the pause and the
// marker insert failure are injected. (Round-3 review, codex-astra.)
func TestSDKConcurrentFallbackDrainsKeepApplicationOrder(t *testing.T) {
	dir := t.TempDir()
	wa.PairedSessionStoreAt(t, filepath.Join(dir, "session.db"))
	a, err := app.New(app.Options{StoreDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	if err := a.OpenWA(); err != nil {
		t.Fatal(err)
	}
	cli := wa.RawClient(a.WA().(*wa.Client))
	pn := types.NewJID("15550002222", types.DefaultUserServer)
	lid := types.NewJID("222222222222222", types.HiddenUserServer)
	if err := cli.Store.LIDs.PutLIDMapping(t.Context(), lid, pn); err != nil {
		t.Fatal(err)
	}
	paused := &pausingLIDStore{LIDStore: cli.Store.LIDs, entered: make(chan struct{}), release: make(chan struct{})}
	cli.Store.LIDs = paused
	remove, err := a.AddChatStatePersistenceHandler(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(remove)
	raw, err := sql.Open("sqlite3", filepath.Join(dir, "wacli.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`CREATE TRIGGER test_fail_intent BEFORE INSERT ON app_state_recovery_intents BEGIN SELECT RAISE(FAIL, 'injected marker failure'); END`); err != nil {
		t.Fatal(err)
	}
	apply := func(jid types.JID, full string) any {
		return cli.DangerousInternals().DispatchAppState(t.Context(), appstate.WAPatchCriticalUnblockLow, appstate.Mutation{
			Operation: waServerSync.SyncdMutation_SET, Index: []string{appstate.IndexContact, jid.String()},
			Action: &waSyncAction.SyncActionValue{ContactAction: &waSyncAction.ContactAction{FullName: proto.String(full)}},
		}, false)
	}
	older := apply(lid, "Earlier Name")
	paused.pause.Store(true)
	firstDone := make(chan struct{})
	go func() { cli.DangerousInternals().DispatchEvent(older); close(firstDone) }()
	<-paused.entered
	newer := apply(pn, "Grace Hopper")
	secondDone := make(chan struct{})
	go func() { cli.DangerousInternals().DispatchEvent(newer); close(secondDone) }()
	select {
	case <-secondDone:
	case <-time.After(200 * time.Millisecond):
	}
	close(paused.release)
	<-firstDone
	<-secondDone
	a.Close() // waits for the ordered replays as well
	var full string
	if err := raw.QueryRow(`SELECT full_name FROM contacts WHERE jid = ?`, pn.String()).Scan(&full); err != nil {
		t.Fatal(err)
	}
	if full != "Grace Hopper" {
		t.Fatalf("wacli.db row for %s = %q, want the newest journaled write Grace Hopper", pn, full)
	}
}

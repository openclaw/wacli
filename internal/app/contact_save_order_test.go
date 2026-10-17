package app

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

const contactCollection = appstate.WAPatchCriticalUnblockLow

func contactEvent(jid types.JID, fullName, firstName string) *events.Contact {
	return &events.Contact{JID: jid, Action: &waSyncAction.ContactAction{
		FullName: proto.String(fullName), FirstName: proto.String(firstName),
	}}
}

func contactRecoveryRequired(t *testing.T, a *App) bool {
	t.Helper()
	required, err := a.db.AppStateRecoveryRequired(string(contactCollection))
	if err != nil {
		t.Fatal(err)
	}
	return required
}

func fullSyncFetches(f *fakeWA, name appstate.WAPatchName) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, fetch := range f.appStateFetches {
		if fetch.name == string(name) && fetch.fullSync {
			n++
		}
	}
	return n
}

// whatsmeow's SendAppState fetches the collection after the send and
// dispatches the fetched batch from a goroutine before it returns. When that
// batch holds our save (indexed by the phone number, as BuildSaveContact
// emits it) followed by a newer phone rename (indexed by the LID), the rename
// is the authoritative state; the optimistic local name must not overwrite it.
func TestSaveContactSameBatchRenameWinsOverOptimisticMirror(t *testing.T) {
	for _, handler := range []string{"sync-follow", "one-shot"} {
		t.Run(handler, func(t *testing.T) {
			a, f := newContactSaveTestApp(t)
			if handler == "sync-follow" {
				installBoundaryReplayHandler(t, a, f)
			} else {
				remove, err := a.AddChatStatePersistenceHandler(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(remove)
			}
			f.saveContactAfterSend = func(call fakeSaveContactCall) {
				f.applyContactBatch(
					contactEvent(call.target, call.fullName, call.firstName),
					contactEvent(testContactLID, "Grace Hopper", "Grace"),
				)()
			}
			if _, err := a.SaveContact(t.Context(), testContactPN, "Ada Lovelace", "", false); err != nil {
				t.Fatal(err)
			}
			if got := storedContact(t, a, testContactPN); got.full != "Grace Hopper" || got.first != "Grace" {
				t.Fatalf("local mirror = %+v, want the newer phone rename Grace Hopper", got)
			}
		})
	}
}

// whatsmeow dispatches every fetched batch from its own goroutine: the 409
// conflict patches, the post-send fetch of the retry that follows them, and
// the post-send fetch of an earlier save all run independently of each other
// and of SendAppState returning. An older batch can therefore reach wacli.db
// after a newer one. whatsmeow writes each batch to the session contact store
// before it dispatches it, so the mirror must follow that store, not the
// event payloads. (Schedules reproduced by the round-1 reviewers.)
func TestSaveContactDelayedOlderBatchDoesNotRegressTheMirror(t *testing.T) {
	t.Run("409 conflict retry", func(t *testing.T) {
		a, f := newContactSaveTestApp(t)
		f.hasLocalAppState = true
		installBoundaryReplayHandler(t, a, f)
		f.saveContactAfterSend = func(fakeSaveContactCall) {
			// sendAppState applies the conflicting older rename first and
			// then, after the retry, the fetch holding our own echo. The two
			// dispatch goroutines run in the opposite order.
			dispatchConflict := f.applyContactBatch(contactEvent(testContactPN, "Grace Hopper", "Grace"))
			dispatchRetry := f.applyContactBatch(contactEvent(testContactPN, "Ada Lovelace", "Ada"))
			dispatchRetry()
			dispatchConflict()
		}
		if _, err := a.SaveContact(t.Context(), testContactPN, "Ada Lovelace", "", false); err != nil {
			t.Fatal(err)
		}
		if got := storedContact(t, a, testContactPN); got.full != "Ada Lovelace" || got.first != "Ada" {
			t.Fatalf("local mirror = %+v, but the retry the server accepted saved Ada Lovelace", got)
		}
	})
	t.Run("two serialized saves", func(t *testing.T) {
		a, f := newContactSaveTestApp(t)
		f.hasLocalAppState = true
		installBoundaryReplayHandler(t, a, f)
		var dispatchFirstEcho func()
		f.saveContactAfterSend = func(call fakeSaveContactCall) {
			dispatch := f.applyContactBatch(contactEvent(call.target, call.fullName, call.firstName))
			if dispatchFirstEcho == nil {
				// The first save returns before its dispatch goroutine runs.
				dispatchFirstEcho = dispatch
				return
			}
			dispatch()
		}
		if _, err := a.SaveContact(t.Context(), testContactPN, "Ada Lovelace", "", false); err != nil {
			t.Fatal(err)
		}
		if _, err := a.SaveContact(t.Context(), testContactPN, "Grace Hopper", "", false); err != nil {
			t.Fatal(err)
		}
		dispatchFirstEcho()
		if got := storedContact(t, a, testContactPN); got.full != "Grace Hopper" || got.first != "Grace" {
			t.Fatalf("local mirror = %+v, but the server applied Grace Hopper last", got)
		}
	})
}

// whatsmeow keeps one contact store row per raw index: our save is indexed by
// the phone number, a phone edit of the same contact may be indexed by its
// LID. Both land on the one wacli.db row, so the mirror must follow the order
// in which the SDK applied the writes across both indices, whatever order
// the events are dispatched in. (Schedules reproduced by the round-2 review.)
func TestSaveContactMixedIndexBatchesFollowApplicationOrder(t *testing.T) {
	t.Run("409 conflict LID then retry PN", func(t *testing.T) {
		a, f := newContactSaveTestApp(t)
		f.hasLocalAppState = true
		installBoundaryReplayHandler(t, a, f)
		f.saveContactAfterSend = func(call fakeSaveContactCall) {
			// The conflicting older phone rename (LID) is applied first, then
			// the retry's own echo (PN); their goroutines run reversed.
			dispatchConflict := f.applyContactBatch(contactEvent(call.lid, "Grace Hopper", "Grace"))
			dispatchRetry := f.applyContactBatch(contactEvent(call.target, call.fullName, call.firstName))
			dispatchRetry()
			dispatchConflict()
		}
		if _, err := a.SaveContact(t.Context(), testContactPN, "Ada Lovelace", "", false); err != nil {
			t.Fatal(err)
		}
		if got := storedContact(t, a, testContactPN); got.full != "Ada Lovelace" || got.first != "Ada" {
			t.Fatalf("local mirror = %+v, but the retry the server accepted saved Ada Lovelace", got)
		}
	})
	t.Run("phone LID rename then delayed PN echo", func(t *testing.T) {
		a, f := newContactSaveTestApp(t)
		f.hasLocalAppState = true
		installBoundaryReplayHandler(t, a, f)
		var dispatchEcho func()
		f.saveContactAfterSend = func(call fakeSaveContactCall) {
			dispatchEcho = f.applyContactBatch(contactEvent(call.target, call.fullName, call.firstName))
		}
		if _, err := a.SaveContact(t.Context(), testContactPN, "Ada Lovelace", "", false); err != nil {
			t.Fatal(err)
		}
		f.applyContactBatch(contactEvent(testContactLID, "Grace Hopper", "Grace"))()
		if got := storedContact(t, a, testContactPN); got.full != "Grace Hopper" {
			t.Fatalf("phone rename not mirrored: %+v", got)
		}
		dispatchEcho()
		if got := storedContact(t, a, testContactPN); got.full != "Grace Hopper" || got.first != "Grace" {
			t.Fatalf("local mirror = %+v after the delayed echo of our save, but the phone renamed to Grace Hopper later", got)
		}
	})
	t.Run("one-shot same batch PN echo then LID rename", func(t *testing.T) {
		a, f := newContactSaveTestApp(t)
		f.hasLocalAppState = true
		remove, err := a.AddChatStatePersistenceHandler(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(remove)
		f.saveContactAfterSend = func(call fakeSaveContactCall) {
			// Consumed before SendAppState returns; the process exits before
			// the dispatch goroutine runs.
			f.applyContactBatch(
				contactEvent(call.target, call.fullName, call.firstName),
				contactEvent(call.lid, "Grace Hopper", "Grace"),
			)
		}
		if _, err := a.SaveContact(t.Context(), testContactPN, "Ada Lovelace", "", false); err != nil {
			t.Fatal(err)
		}
		remove()
		got, debt := storedContact(t, a, testContactPN), contactRecoveryRequired(t, a)
		if got.full != "Grace Hopper" || got.first != "Grace" {
			t.Fatalf("local mirror = %+v, want the phone rename consumed with the save (debt=%v)", got, debt)
		}
		if debt {
			t.Fatal("critical_unblock_low debt kept after the consumed batch was mirrored")
		}
	})
}

// A ContactAction without names is an authoritative clear: whatsmeow stores
// empty names for the existing row. A clear consumed with a save, or left
// unmirrored when the process exited, must reach wacli.db like any rename.
// (Reproduced by the round-2 review.)
func TestConsumedClearNameIsMirrored(t *testing.T) {
	for _, path := range []string{"one-shot", "startup", "pre-write"} {
		t.Run(path, func(t *testing.T) {
			a, f := newContactSaveTestApp(t)
			f.hasLocalAppState = true
			remove, err := a.AddChatStatePersistenceHandler(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(remove)
			other := types.NewJID("15550002222", types.DefaultUserServer)
			f.applyContactBatch(contactEvent(other, "Grace Hopper", "Grace"))()
			if got := storedContact(t, a, other); got.full != "Grace Hopper" {
				t.Fatalf("test setup: %+v", got)
			}
			if path == "one-shot" {
				f.saveContactAfterSend = func(call fakeSaveContactCall) {
					f.applyContactBatch(contactEvent(call.target, call.fullName, call.firstName), contactEvent(other, "", ""))
				}
				if _, err := a.SaveContact(t.Context(), testContactPN, "Ada Lovelace", "", false); err != nil {
					t.Fatal(err)
				}
				remove()
			} else {
				if _, err := a.db.MarkAppStateRecoveryGeneration(string(contactCollection)); err != nil {
					t.Fatal(err)
				}
				f.applyContactBatch(contactEvent(other, "", ""))
				f.onAppStateRecovery = func(string) {}
				if path == "startup" {
					a.syncAppStateDeltas(t.Context(), &sync.Map{})
				} else if err := a.syncChatStateBeforeWrite(t.Context(), contactCollection); err != nil {
					t.Fatal(err)
				}
			}
			if got := storedContact(t, a, other); got.full != "" || got.first != "" {
				t.Fatalf("consumed clear not mirrored: %+v (debt=%v)", got, contactRecoveryRequired(t, a))
			}
		})
	}
}

// Saving one contact must not touch other contacts' names: a clear already
// mirrored from a LID-indexed action stays cleared even though the stale
// phone-number row still exists in whatsmeow's store. (Round-2 review.)
func TestSaveContactDoesNotResurrectAClearedAlias(t *testing.T) {
	a, f := newContactSaveTestApp(t)
	f.hasLocalAppState = true
	installBoundaryReplayHandler(t, a, f)
	other := types.NewJID("15550002222", types.DefaultUserServer)
	lid := types.NewJID("222222222222222", types.HiddenUserServer)
	f.lids[lid] = other
	f.applyContactBatch(contactEvent(other, "Old Name", "Old"))()
	f.applyContactBatch(contactEvent(lid, "", ""))()
	if got := storedContact(t, a, other); got.full != "" || got.first != "" {
		t.Fatalf("test setup: LID clear not mirrored: %+v", got)
	}
	f.saveContactAfterSend = func(call fakeSaveContactCall) {
		f.applyContactBatch(contactEvent(call.target, call.fullName, call.firstName))()
	}
	if _, err := a.SaveContact(t.Context(), testContactPN, "Ada Lovelace", "", false); err != nil {
		t.Fatal(err)
	}
	if got := storedContact(t, a, other); got.full != "" || got.first != "" {
		t.Fatalf("saving an unrelated contact resurrected cleared names: %+v", got)
	}
}

// A phone rename indexed by a LID, consumed alongside a save of another
// contact and never dispatched (one-shot exit), must be mirrored under the
// mapped phone number; the stale phone-number row in whatsmeow's store is
// older and must not win. (Round-2 review.)
func TestSaveContactMirrorsAnAliasRenameConsumedWithTheSave(t *testing.T) {
	a, f := newContactSaveTestApp(t)
	f.hasLocalAppState = true
	remove, err := a.AddChatStatePersistenceHandler(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(remove)
	other := types.NewJID("15550002222", types.DefaultUserServer)
	lid := types.NewJID("222222222222222", types.HiddenUserServer)
	f.lids[lid] = other
	f.applyContactBatch(contactEvent(other, "Old Name", "Old"))()
	f.saveContactAfterSend = func(call fakeSaveContactCall) {
		f.applyContactBatch(
			contactEvent(call.target, call.fullName, call.firstName),
			contactEvent(lid, "Grace Hopper", "Grace"),
		)
	}
	if _, err := a.SaveContact(t.Context(), testContactPN, "Ada Lovelace", "", false); err != nil {
		t.Fatal(err)
	}
	remove()
	got, debt := storedContact(t, a, other), contactRecoveryRequired(t, a)
	if got.full != "Grace Hopper" || got.first != "Grace" {
		t.Fatalf("alias rename consumed with the save not mirrored: %+v (debt=%v)", got, debt)
	}
	if debt {
		t.Fatal("critical_unblock_low debt kept after the consumed batch was mirrored")
	}
}

// whatsmeow's PutContactName can fail (a cancelled context, a busy store)
// while dispatchAppState still emits the event and the collection cursor has
// advanced: the decoded mutation is WhatsApp's state. The intended names must
// reach wacli.db; the stale store row must not be mirrored instead, and the
// event must not be dropped. (Round-2 review; the real-SDK behaviour is
// pinned in internal/wa.)
func TestContactEventSurvivesFailedSessionStoreWrite(t *testing.T) {
	a, f := newContactSaveTestApp(t)
	f.hasLocalAppState = true
	installBoundaryReplayHandler(t, a, f)
	f.applyContactBatch(contactEvent(testContactPN, "Old Name", "Old"))()
	f.mu.Lock()
	f.contactStoreErr[testContactPN] = context.Canceled
	f.mu.Unlock()
	f.applyContactBatch(contactEvent(testContactPN, "Grace Hopper", "Grace"))()
	if info, _ := f.GetContact(t.Context(), testContactPN); info.FullName != "Old Name" {
		t.Fatalf("test setup: the fake store write did not fail: %+v", info)
	}
	if got := storedContact(t, a, testContactPN); got.full != "Grace Hopper" || got.first != "Grace" {
		t.Fatalf("local mirror = %+v after a failed session store write; the decoded mutation says Grace Hopper", got)
	}
	if contactRecoveryRequired(t, a) {
		t.Fatal("debt left for a mutation that was mirrored")
	}
}

// Two concurrent saves of the same contact (two delegated requests) must
// leave the name of the save the server applied last.
func TestSaveContactConcurrentSavesMirrorTheLastSend(t *testing.T) {
	a, f := newContactSaveTestApp(t)
	firstSent := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondSent := make(chan struct{})
	var calls int
	var mu sync.Mutex
	f.saveContactAfterSend = func(fakeSaveContactCall) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			close(firstSent)
			<-releaseFirst
			return
		}
		close(secondSent)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Go(func() {
		_, err := a.SaveContact(t.Context(), testContactPN, "Ada Lovelace", "", false)
		errs <- err
	})
	<-firstSent
	wg.Go(func() {
		_, err := a.SaveContact(t.Context(), testContactPN, "Grace Hopper", "", false)
		errs <- err
	})
	// Unserialized, the second save sends and mirrors while the first is
	// still in flight; serialized, it waits for the first to finish.
	select {
	case <-secondSent:
	case <-time.After(300 * time.Millisecond):
	}
	close(releaseFirst)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	f.mu.Lock()
	calls2 := append([]fakeSaveContactCall(nil), f.saveContactCalls...)
	f.mu.Unlock()
	if len(calls2) != 2 {
		t.Fatalf("calls = %+v", calls2)
	}
	if got, last := storedContact(t, a, testContactPN), calls2[1].fullName; got.full != last {
		t.Fatalf("local mirror = %q, but the server applied %q last", got.full, last)
	}
}

// App-state writes bypass the paced send queue because the app serializes
// them (#454). A contact save must wait for an in-flight app-state write and
// its pre-write synchronization instead of sending alongside it.
func TestSaveContactSerializesWithAppStateWrites(t *testing.T) {
	a, f := newContactSaveTestApp(t)
	release, err := a.acquireChatStateSync(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := a.SaveContact(t.Context(), testContactPN, "Ada Lovelace", "", false)
		done <- err
	}()
	time.Sleep(200 * time.Millisecond)
	f.mu.Lock()
	sent := len(f.saveContactCalls)
	f.mu.Unlock()
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if sent != 0 {
		t.Fatal("contacts save sent while another app-state write held the app-state coordinator")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.appStateFetches) == 0 || f.appStateFetches[0].name != string(contactCollection) {
		t.Fatalf("contacts save did not synchronize %s before writing: %+v", contactCollection, f.appStateFetches)
	}
}

// A one-shot save returns, disconnects and removes its persistence handler
// before whatsmeow's dispatch goroutine runs, so the events of the post-send
// batch never reach wacli.db. whatsmeow wrote that batch to its contact store
// before SendAppState returned, so the save mirrors the journaled writes
// before it returns, and the debt recorded before the send is settled without
// a recovery snapshot.
func TestSaveContactOneShotMirrorsThePostSendBatchBeforeExit(t *testing.T) {
	a, f := newContactSaveTestApp(t)
	f.hasLocalAppState = true
	other := types.NewJID("15550002222", types.DefaultUserServer)
	var dispatch func()
	f.saveContactAfterSend = func(call fakeSaveContactCall) {
		dispatch = f.applyContactBatch(
			contactEvent(call.target, call.fullName, call.firstName),
			contactEvent(other, "Grace Hopper", "Grace"),
		)
	}
	remove, err := a.AddChatStatePersistenceHandler(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.SaveContact(t.Context(), testContactPN, "Ada Lovelace", "", false); err != nil {
		t.Fatal(err)
	}
	remove()
	dispatch() // nobody is listening any more
	if got := storedContact(t, a, other); got.full != "Grace Hopper" {
		t.Fatalf("edit fetched with the save = %+v, want Grace Hopper mirrored before the save returned", got)
	}
	if contactRecoveryRequired(t, a) {
		t.Fatal("critical_unblock_low debt kept after the save mirrored the batch")
	}
	if got := recoveredCollections(f); len(got) != 0 {
		t.Fatalf("recovery snapshots requested: %v", got)
	}
}

// The residual window: a save dies after whatsmeow consumed the post-send
// batch (cursor and contact store persisted) and before the save mirrored
// it. The in-memory journal dies with the process; only the write-ahead debt
// survives. The next start must not settle that debt from the contact store
// (its rows carry no order across a contact's phone-number and LID indices,
// and cannot tell a cleared name from a never-saved one); it keeps the debt,
// requests the primary's snapshot without a full sync, and settles when the
// snapshot's mutations have been mirrored.
func TestContactDebtLeftByAnInterruptedSaveWaitsForTheSnapshot(t *testing.T) {
	a, f := newContactSaveTestApp(t)
	f.hasLocalAppState = true
	other := types.NewJID("15550002222", types.DefaultUserServer)
	// The dead process consumed this batch: the SDK store has it, the journal
	// does not, and the debt its save recorded before the send remains.
	f.applyContactBatch(contactEvent(testContactLID, "Ada Lovelace", "Ada"), contactEvent(other, "Grace Hopper", "Grace"))
	f.journal.Ack(f.journal.HighWater())
	if _, err := a.db.MarkAppStateRecoveryGeneration(string(contactCollection)); err != nil {
		t.Fatal(err)
	}
	answered := make(chan struct{})
	f.onAppStateRecovery = func(name string) {
		go func() {
			// The primary's snapshot replays the collection: whatsmeow writes
			// every contact to its store and dispatches the events.
			f.applyContactBatch(contactEvent(testContactLID, "Ada Lovelace", "Ada"), contactEvent(other, "Grace Hopper", "Grace"))()
			f.emit(&events.AppStateSyncComplete{Name: appstate.WAPatchName(name), Recovery: true})
			close(answered)
		}()
	}
	installBoundaryReplayHandler(t, a, f)
	a.syncAppStateDeltas(t.Context(), &sync.Map{})
	if n := fullSyncFetches(f, contactCollection); n != 0 {
		t.Fatalf("startup debt ran %d destructive full syncs of %s", n, contactCollection)
	}
	if got := recoveredCollections(f); !slices.Equal(got, []string{string(contactCollection)}) {
		t.Fatalf("recoveries = %v, want the primary's snapshot requested for the interrupted save", got)
	}
	<-answered
	deadline := time.Now().Add(5 * time.Second)
	for contactRecoveryRequired(t, a) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if contactRecoveryRequired(t, a) {
		t.Fatal("the mirrored snapshot did not settle the contact debt")
	}
	if got := storedContact(t, a, other); got.full != "Grace Hopper" || got.first != "Grace" {
		t.Fatalf("edit consumed by the dead process = %+v, want Grace Hopper from the snapshot", got)
	}
}

// Debt a dead process left (see above) is older than the next save's own
// intent. That save settles only what it mirrored itself: its own intent,
// never the older generation it cannot vouch for. (Round-2 review, P3-1.)
func TestSaveContactKeepsOlderContactDebt(t *testing.T) {
	a, f := newContactSaveTestApp(t)
	f.hasLocalAppState = true
	f.onAppStateRecovery = func(string) {}
	older, err := a.db.MarkAppStateRecoveryGeneration(string(contactCollection))
	if err != nil {
		t.Fatal(err)
	}
	f.saveContactAfterSend = func(call fakeSaveContactCall) {
		f.applyContactBatch(contactEvent(call.target, call.fullName, call.firstName))()
	}
	if _, err := a.SaveContact(t.Context(), testContactPN, "Ada Lovelace", "", false); err != nil {
		t.Fatal(err)
	}
	if n := fullSyncFetches(f, contactCollection); n != 0 {
		t.Fatalf("the save ran %d destructive full syncs of %s", n, contactCollection)
	}
	if !recoveryIntentExists(t, a, string(contactCollection), older) {
		t.Fatal("the save settled debt left by an interrupted process, which it did not mirror")
	}
	if got := storedContact(t, a, testContactPN); got.full != "Ada Lovelace" {
		t.Fatalf("local mirror = %+v", got)
	}
}

// An ordinary missing key is requested by whatsmeow when decoding fails and
// usually arrives before the next fetch. A write must keep retrying the
// (non-destructive) fetch until it does, as docs/sync.md promises, instead
// of waiting for a recovery snapshot the phone may never send: on the contact
// collection, which is never full-synced while local state exists, and on a
// flagged collection, which keeps main's behaviour. (Reproduced by all three
// round-1 reviewers.)
func TestOrdinaryMissingKeyRetriesWithoutSnapshot(t *testing.T) {
	cases := []struct {
		name       string
		collection appstate.WAPatchName
		flagged    bool
		debt       bool
	}{
		{name: "contacts", collection: contactCollection},
		{name: "contacts with debt", collection: contactCollection, debt: true},
		{name: "flagged regular_low", collection: appstate.WAPatchRegularLow, flagged: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, f := newContactSaveTestApp(t)
			f.hasLocalAppState = true
			if tc.flagged {
				a.setAppStateFullSyncResult(tc.collection, true)
			}
			if tc.debt {
				if _, err := a.db.MarkAppStateRecoveryGeneration(string(tc.collection)); err != nil {
					t.Fatal(err)
				}
			}
			f.appStateFetchErrs = []error{appstate.ErrKeyNotFound, nil}
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			if err := a.syncChatStateBeforeWrite(ctx, tc.collection); err != nil {
				t.Fatalf("the key available at the next fetch was never retried (recoveries %v): %v", recoveredCollections(f), err)
			}
			if got := recoveredCollections(f); !tc.debt && len(got) != 0 {
				t.Fatalf("recovery snapshots requested for an ordinary missing key: %v", got)
			}
			if tc.collection == contactCollection {
				if n := fullSyncFetches(f, tc.collection); n != 0 {
					t.Fatalf("missing key ran %d destructive full syncs of %s", n, tc.collection)
				}
			}
		})
	}
}

// With stock whatsmeow a full sync deletes the saved collection version
// before it decodes the snapshot, so a snapshot that does not verify leaves
// the contact collection empty until the primary answers. The contact
// collection is repaired from the primary's recovery snapshot instead,
// keeping local state, whenever local state exists.
func TestContactCollectionRecoveryKeepsLocalState(t *testing.T) {
	answerRecovery := func(f *fakeWA) {
		f.onAppStateRecovery = func(name string) {
			go f.emit(&events.AppStateSyncComplete{Name: appstate.WAPatchName(name), Recovery: true})
		}
	}
	t.Run("live mismatch", func(t *testing.T) {
		a, f := newContactSaveTestApp(t)
		f.hasLocalAppState = true
		answerRecovery(f)
		a.recoverAppStateCollection(t.Context(), string(contactCollection), &sync.Map{}, 2*time.Second)
		if n := fullSyncFetches(f, contactCollection); n != 0 {
			t.Fatalf("live LTHash mismatch ran %d destructive full syncs of %s", n, contactCollection)
		}
		if got := recoveredCollections(f); !slices.Equal(got, []string{string(contactCollection)}) {
			t.Fatalf("recoveries = %v", got)
		}
	})
	t.Run("pre-write mismatch", func(t *testing.T) {
		a, f := newContactSaveTestApp(t)
		f.hasLocalAppState = true
		answerRecovery(f)
		f.appStateFetchErrs = []error{appstate.ErrMismatchingLTHash}
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		if err := a.syncChatStateBeforeWrite(ctx, contactCollection); err != nil {
			t.Fatal(err)
		}
		if n := fullSyncFetches(f, contactCollection); n != 0 {
			t.Fatalf("pre-write LTHash mismatch ran %d destructive full syncs of %s", n, contactCollection)
		}
		if got := recoveredCollections(f); !slices.Equal(got, []string{string(contactCollection)}) {
			t.Fatalf("recoveries = %v", got)
		}
	})
	t.Run("startup debt", func(t *testing.T) {
		a, f := newContactSaveTestApp(t)
		f.hasLocalAppState = true
		answerRecovery(f)
		if _, err := a.db.MarkAppStateRecoveryGeneration(string(contactCollection)); err != nil {
			t.Fatal(err)
		}
		a.syncAppStateDeltas(t.Context(), &sync.Map{})
		if n := fullSyncFetches(f, contactCollection); n != 0 {
			t.Fatalf("startup replay debt ran %d destructive full syncs of %s", n, contactCollection)
		}
	})
	t.Run("write after a save", func(t *testing.T) {
		a, f := newContactSaveTestApp(t)
		f.hasLocalAppState = true
		answerRecovery(f)
		for _, name := range []string{"Ada Lovelace", "Grace Hopper"} {
			if _, err := a.SaveContact(t.Context(), testContactPN, name, "", false); err != nil {
				t.Fatal(err)
			}
		}
		if n := fullSyncFetches(f, contactCollection); n != 0 {
			t.Fatalf("the save after a save ran %d destructive full syncs of %s", n, contactCollection)
		}
	})
	t.Run("no local state", func(t *testing.T) {
		// Nothing to lose: a full sync is the only way to get the collection.
		a, f := newContactSaveTestApp(t)
		a.recoverAppStateCollection(t.Context(), string(contactCollection), &sync.Map{}, time.Second)
		if n := fullSyncFetches(f, contactCollection); n != 1 {
			t.Fatalf("full syncs = %d, want 1 without local state", n)
		}
	})
}

// Only the contact collection skips the full sync on a live hash failure. A
// chat collection flagged by #497 keeps main's recovery (one full replay,
// then the snapshot); changing that is for #497's follow-ups, not for
// contacts.
func TestFlaggedChatCollectionLiveMismatchKeepsMainRecovery(t *testing.T) {
	a, f := newContactSaveTestApp(t)
	f.hasLocalAppState = true
	a.setAppStateFullSyncResult(appstate.WAPatchRegularLow, true)
	a.recoverAppStateCollection(t.Context(), string(appstate.WAPatchRegularLow), &sync.Map{}, 2*time.Second)
	if n := fullSyncFetches(f, appstate.WAPatchRegularLow); n != 1 {
		t.Fatalf("live LTHash mismatch on a flagged chat collection ran %d full syncs, want main's 1", n)
	}
	if got := recoveredCollections(f); len(got) != 0 {
		t.Fatalf("recoveries = %v, want none after the full sync verified", got)
	}
}

package app

import (
	"context"
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
		// Two patches in the send window: not known to be covered.
		if !debt {
			t.Fatal("critical_unblock_low debt settled although the send window held more than the save's own patch")
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
	// Two patches in the send window: not known to be covered.
	if !debt {
		t.Fatal("critical_unblock_low debt settled although the send window held more than the save's own patch")
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
// before it returns. When the batch held only the save's own patch, the debt
// recorded before the send is settled without a recovery snapshot; with
// another patch in it, the debt is kept (see
// TestSaveContactKeepsDebtWhenTheSendWindowIsNotCovered).
func TestSaveContactOneShotMirrorsThePostSendBatchBeforeExit(t *testing.T) {
	for _, withOther := range []bool{false, true} {
		t.Run(map[bool]string{false: "only its own patch", true: "with another contact's edit"}[withOther], func(t *testing.T) {
			a, f := newContactSaveTestApp(t)
			f.hasLocalAppState = true
			other := types.NewJID("15550002222", types.DefaultUserServer)
			var dispatch func()
			f.saveContactAfterSend = func(call fakeSaveContactCall) {
				evts := []*events.Contact{contactEvent(call.target, call.fullName, call.firstName)}
				if withOther {
					evts = append(evts, contactEvent(other, "Grace Hopper", "Grace"))
				}
				dispatch = f.applyContactBatch(evts...)
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
			if got := storedContact(t, a, testContactPN); got.full != "Ada Lovelace" {
				t.Fatalf("save's echo = %+v, want it mirrored before the save returned", got)
			}
			if got := storedContact(t, a, other); withOther && got.full != "Grace Hopper" {
				t.Fatalf("edit fetched with the save = %+v, want Grace Hopper mirrored before the save returned", got)
			}
			if debt := contactRecoveryRequired(t, a); debt != withOther {
				t.Fatalf("critical_unblock_low debt = %v, want %v", debt, withOther)
			}
			if got := recoveredCollections(f); len(got) != 0 {
				t.Fatalf("recovery snapshots requested: %v", got)
			}
		})
	}
}

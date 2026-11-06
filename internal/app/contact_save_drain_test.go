package app

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/sqliteutil"
	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/proto/waServerSync"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func openRawWacliDB(t *testing.T, a *App) *sql.DB {
	t.Helper()
	raw, err := sql.Open("sqlite3", filepath.Join(a.StoreDir(), "wacli.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	return raw
}

const failRecoveryIntentInsert = `CREATE TRIGGER test_fail_intent BEFORE INSERT ON app_state_recovery_intents BEGIN SELECT RAISE(FAIL, 'injected marker failure'); END`

type beforeBoundaryWA struct {
	*fakeWA
	beforeBoundary func()
}

func (w *beforeBoundaryWA) SendAppStatePatch(ctx context.Context, patch appstate.PatchInfo, beforeApply func()) ([]any, error) {
	w.beforeBoundary()
	return w.fakeWA.SendAppStatePatch(ctx, patch, beforeApply)
}

// The same schedule with the pinned SDK's dispatchAppState over a real
// session sqlstore: the earlier event reserves its live ticket before the
// send boundary while its recovery marker waits for SQLite, then drains the
// send's batch. (Round-3 review, codex-sol.)
func TestSaveContactEarlierEventDrainWithSDKDispatch(t *testing.T) {
	a, f := newContactSaveTestApp(t)
	f.hasLocalAppState = true
	container, err := sqlstore.New(t.Context(), "sqlite3", sqliteutil.FileURI(filepath.Join(t.TempDir(), "session.db"), "_foreign_keys=on"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer container.Close()
	device := container.NewDevice()
	device.ID = &types.JID{User: "15550000000", Device: 7, Server: types.DefaultUserServer}
	device.Account = &waAdv.ADVSignedDeviceIdentity{Details: []byte{1}, AccountSignature: make([]byte, 64), AccountSignatureKey: make([]byte, 32), DeviceSignature: make([]byte, 64)}
	if err := device.Save(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.journal = wa.NewContactStoreJournal(device.Contacts)
	device.Contacts = f.journal
	sdk := whatsmeow.NewClient(device, nil)
	apply := func(evts ...*events.Contact) func() {
		batch := make([]any, 0, len(evts))
		for _, evt := range evts {
			got := sdk.DangerousInternals().DispatchAppState(t.Context(), contactCollection, appstate.Mutation{
				Operation: waServerSync.SyncdMutation_SET,
				Index:     []string{appstate.IndexContact, evt.JID.String()},
				Action:    &waSyncAction.SyncActionValue{ContactAction: &waSyncAction.ContactAction{FullName: proto.String(evt.Action.GetFullName()), FirstName: proto.String(evt.Action.GetFirstName())}},
			}, false)
			if _, ok := got.(*events.Contact); !ok {
				t.Fatalf("SDK returned %T", got)
			}
			batch = append(batch, got)
		}
		return func() { f.dispatchAsync(batch...) }
	}
	w := &beforeBoundaryWA{fakeWA: f}
	a.wa = w
	remove, err := a.AddChatStatePersistenceHandler(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(remove)
	raw := openRawWacliDB(t, a)
	conn, err := raw.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	eventDone := make(chan struct{})
	var lateDispatch func()
	w.beforeBoundary = func() {
		dispatch := apply(contactEvent(testContactPN, "Old Name", "Old"))
		if _, err := conn.ExecContext(t.Context(), "BEGIN IMMEDIATE"); err != nil {
			t.Fatal(err)
		}
		a.appStatePersist.mu.Lock()
		next := a.appStatePersist.next
		a.appStatePersist.mu.Unlock()
		go func() { dispatch(); close(eventDone) }()
		deadline := time.Now().Add(2 * time.Second)
		for {
			a.appStatePersist.mu.Lock()
			reserved := a.appStatePersist.next > next
			a.appStatePersist.mu.Unlock()
			if reserved {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("earlier event did not reserve a ticket")
			}
			time.Sleep(time.Millisecond)
		}
	}
	f.saveContactAfterSend = func(call fakeSaveContactCall) {
		lateDispatch = apply(contactEvent(call.target, call.fullName, call.firstName), contactEvent(call.lid, "Grace Hopper", "Grace"))
		if _, err := conn.ExecContext(t.Context(), "COMMIT"); err != nil {
			t.Fatal(err)
		}
		select {
		case <-eventDone:
		case <-time.After(5 * time.Second):
			t.Fatal("earlier event did not finish")
		}
	}
	if _, err := a.SaveContact(t.Context(), testContactPN, "Ada Lovelace", "", false); err != nil {
		t.Fatal(err)
	}
	lateDispatch()
	if got := storedContact(t, a, testContactPN); got.full != "Grace Hopper" {
		t.Fatalf("local mirror = %+v, want the phone rename the SDK applied after our save", got)
	}
}

// When the live recovery marker cannot be written, the event handler drains
// once immediately, outside the ticket order, and again in order. That early
// drain may mirror the send's whole batch before the save's task runs; the
// save must not overwrite it. (Round-3 review, codex-astra.)
func TestSaveContactMarkerFailureFallbackKeepsApplicationOrder(t *testing.T) {
	a, f := newContactSaveTestApp(t)
	installBoundaryReplayHandler(t, a, f)
	raw := openRawWacliDB(t, a)
	f.saveContactAfterSend = func(call fakeSaveContactCall) {
		if _, err := raw.Exec(failRecoveryIntentInsert); err != nil {
			t.Fatal(err)
		}
		f.applyContactBatch(contactEvent(call.target, call.fullName, call.firstName), contactEvent(call.lid, "Grace Hopper", "Grace"))()
		if _, err := raw.Exec(`DROP TRIGGER test_fail_intent`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.SaveContact(t.Context(), testContactPN, "Ada Lovelace", "", false); err != nil {
		t.Fatal(err)
	}
	if got := storedContact(t, a, testContactPN); got.full != "Grace Hopper" {
		t.Fatalf("local mirror = %+v, want the phone rename the SDK applied after our save", got)
	}
}

// A send that consumed nothing should not leave debt that forces an
// unnecessary full replay before the next write.
func TestSaveContactFailedSendSettlesItsOwnDebt(t *testing.T) {
	a, f := newContactSaveTestApp(t)
	f.hasLocalAppState = true
	f.onAppStateRecovery = func(string) {}
	f.saveContactErr = errors.New("boom")
	if _, err := a.SaveContact(t.Context(), testContactPN, "Ada Lovelace", "", false); err == nil {
		t.Fatal("save succeeded although the send failed")
	}
	if contactRecoveryRequired(t, a) {
		t.Fatal("a send that consumed nothing left contact debt behind")
	}
	f.saveContactErr = nil
	if _, err := a.SaveContact(t.Context(), testContactPN, "Ada Lovelace", "", false); err != nil {
		t.Fatal(err)
	}
	if got := recoveredCollections(f); len(got) != 0 {
		t.Fatalf("recovery snapshots requested after a failed save: %v", got)
	}
	if contactRecoveryRequired(t, a) {
		t.Fatal("contact debt kept after a successful save")
	}
}

// whatsmeow applies the 409 conflict patches (writing its contact store)
// before it retries, so a send that fails afterwards has still consumed that
// batch. A one-shot save exits before the batch's events are dispatched; the
// save must mirror it before returning the error. The cursor moved during the
// failed send, and whatsmeow can move it past names it never applied (see
// TestSaveContactFailedSendAfterPartialDecodeKeepsDebt), so the save keeps
// its recovery intent.
func TestSaveContactFailedSendMirrorsTheBatchItConsumed(t *testing.T) {
	a, f := newContactSaveTestApp(t)
	f.hasLocalAppState = true
	remove, err := a.AddChatStatePersistenceHandler(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(remove)
	f.saveContactAfterSend = func(fakeSaveContactCall) {
		f.applyContactBatch(contactEvent(testContactLID, "Grace Hopper", "Grace"))
	}
	f.saveContactErrAfterSend = errors.New("retry rejected")
	if _, err := a.SaveContact(t.Context(), testContactPN, "Ada Lovelace", "", false); err == nil {
		t.Fatal("save succeeded although the send failed")
	}
	if got := storedContact(t, a, testContactPN); got.full != "Grace Hopper" || got.first != "Grace" {
		t.Fatalf("local mirror = %+v, want the conflict batch whatsmeow applied before the failure", got)
	}
	if !contactRecoveryRequired(t, a) {
		t.Fatal("contact debt settled although the collection cursor moved during the failed send")
	}
}

// A drain that fails keeps the save's debt; a later drain that mirrors the
// same writes settles it in the same process, without a recovery snapshot.
func TestSaveContactDrainFailureKeepsDebtUntilALaterDrainCoversIt(t *testing.T) {
	a, f := newContactSaveTestApp(t)
	f.hasLocalAppState = true
	f.onAppStateRecovery = func(string) {}
	raw := openRawWacliDB(t, a)
	if _, err := raw.Exec(`CREATE TRIGGER test_fail_target BEFORE INSERT ON contacts WHEN NEW.jid = '` + testContactPN.String() + `' BEGIN SELECT RAISE(FAIL, 'injected mirror failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SaveContact(t.Context(), testContactPN, "Ada Lovelace", "", false); err != nil {
		t.Fatal(err)
	}
	if !contactRecoveryRequired(t, a) {
		t.Fatal("contact debt settled although the save's batch was not mirrored")
	}
	if got := storedContact(t, a, testContactPN); got.found {
		t.Fatalf("test setup: the injected failure did not stop the drain: %+v", got)
	}
	if _, err := raw.Exec(`DROP TRIGGER test_fail_target`); err != nil {
		t.Fatal(err)
	}
	remove, err := a.AddChatStatePersistenceHandler(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(remove)
	f.applyContactBatch(contactEvent(types.NewJID("15550003333", types.DefaultUserServer), "Lin Wei", "Lin"))()
	if got := storedContact(t, a, testContactPN); got.full != "Ada Lovelace" {
		t.Fatalf("later drain did not retry the failed write: %+v", got)
	}
	if contactRecoveryRequired(t, a) {
		t.Fatal("contact debt kept after a later drain mirrored the save's writes")
	}
	if got := recoveredCollections(f); len(got) != 0 {
		t.Fatalf("recovery snapshots requested: %v", got)
	}
}

// Pins that a drain which runs while
// the send is in flight (an unrelated older batch, drained by the
// marker-failure fallback or by any other task) cannot settle the save's
// debt: the debt is registered after the send, with the send's own writes as
// its reach. (Round-4 audit, implementer-fable.)
func TestSaveContactDebtIsNotSettledByADrainDuringTheSend(t *testing.T) {
	a, f := newContactSaveTestApp(t)
	f.hasLocalAppState = true
	f.onAppStateRecovery = func(string) {}
	other := types.NewJID("15550002222", types.DefaultUserServer)
	raw := openRawWacliDB(t, a)
	// An older batch's store write is journaled before the save; its event is late.
	f.applyContactBatch(contactEvent(other, "Earlier Name", "Earlier"))
	f.saveContactAfterSend = func(call fakeSaveContactCall) {
		// A drain of the older batch while the send is in flight (what the
		// marker-failure fallback does outside the ticket order).
		if _, err := a.drainContactJournal(context.Background()); err != nil {
			t.Fatal(err)
		}
		// The send's own batch lands afterwards, and its mirror will fail.
		f.applyContactBatch(contactEvent(call.target, call.fullName, call.firstName))
		if _, err := raw.Exec(`CREATE TRIGGER test_fail_save BEFORE INSERT ON contacts WHEN NEW.jid = '` + call.target.String() + `' BEGIN SELECT RAISE(FAIL, 'injected mirror failure'); END`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.SaveContact(t.Context(), testContactPN, "Ada Lovelace", "", false); err != nil {
		t.Fatal(err)
	}
	if got := storedContact(t, a, testContactPN); got.found {
		t.Fatalf("test setup: the injected failure did not stop the save's drain: %+v", got)
	}
	if !contactRecoveryRequired(t, a) {
		t.Fatal("contact debt settled by a drain that ran before the send's writes existed")
	}
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/delegate"
	"github.com/openclaw/wacli/internal/lock"
	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

// Synthetic proof for delegated delete-for-everyone. Every chat, sender and
// message ID is a fixture, and the native call is a recorder: nothing reaches
// WhatsApp.

const (
	delPN    = "15550001001@s.whatsapp.net"
	delLID   = "900000001@lid"
	delOther = "15550001002@s.whatsapp.net"
	delGroup = "120363000000000001@g.us"
	delSelf  = "15550000000@s.whatsapp.net"
	delID    = "3EB0A1B2C3D4E5F6"
	delNext  = "3EB0A1B2C3D4E5F7"
)

func openDeleteStore(t *testing.T, dir string) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(dir, "wacli.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// putDeleteRow stores one of this account's undeleted messages; edit can
// change that.
func putDeleteRow(t *testing.T, db *store.DB, chat, id string, edit func(*store.UpsertMessageParams)) {
	t.Helper()
	ts := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	kind := "dm"
	if strings.HasSuffix(chat, "@g.us") {
		kind = "group"
	}
	if err := db.UpsertChat(chat, kind, "", ts); err != nil {
		t.Fatalf("store chat: %v", err)
	}
	p := store.UpsertMessageParams{ChatJID: chat, MsgID: id, SenderJID: delSelf, Timestamp: ts, FromMe: true, Text: "fixture"}
	if edit != nil {
		edit(&p)
	}
	if err := db.UpsertMessage(p); err != nil {
		t.Fatalf("store message: %v", err)
	}
}

func fromSomeoneElse(p *store.UpsertMessageParams) {
	p.FromMe = false
	p.SenderJID = delPN
}

func revokedState(t *testing.T, db *store.DB, chat, id string) bool {
	t.Helper()
	msg, err := db.GetMessage(chat, id)
	if err != nil {
		t.Fatalf("read %s %s: %v", chat, id, err)
	}
	return msg.Revoked
}

type revokeRecorder struct {
	mu      sync.Mutex
	calls   []string
	err     error
	started chan struct{}
	hold    chan struct{}
}

// revoke records the native call, then waits for hold when it is set,
// ignoring its context the way a blocked SDK send can.
func (r *revokeRecorder) revoke(_ context.Context, chat types.JID, id types.MessageID) (types.MessageID, error) {
	r.mu.Lock()
	r.calls = append(r.calls, chat.String()+" "+string(id))
	err, started, hold := r.err, r.started, r.hold
	r.mu.Unlock()
	if started != nil {
		started <- struct{}{}
	}
	if hold != nil {
		<-hold
	}
	if err != nil {
		return "", err
	}
	return "REVOKE-1", nil
}

func (r *revokeRecorder) calledWith() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

func deleteNoAlias(context.Context, types.JID) (types.JID, error) { return types.EmptyJID, nil }

// deletePairAlias is a stored mapping between one phone number and one LID.
func deletePairAlias(pn, lid string) chatAliasLookup {
	p, _ := types.ParseJID(pn)
	l, _ := types.ParseJID(lid)
	return func(_ context.Context, jid types.JID) (types.JID, error) {
		switch jid.ToNonAD() {
		case p:
			return l, nil
		case l:
			return p, nil
		}
		return types.EmptyJID, nil
	}
}

func testDelegatedDelete(storeDir string, db deleteStore, alias chatAliasLookup, rec *revokeRecorder) delegatedDelete {
	return delegatedDelete{db: db, storeDir: storeDir, connected: func() bool { return true }, alias: alias, revoke: rec.revoke}
}

func boundDelete(to, target, id string) sendDelegateRequest {
	return sendDelegateRequest{Version: sendDelegateVersion, Kind: deleteKind, To: to, TargetChat: target, ID: id}
}

func heldLockErr() error { return fmt.Errorf("store is locked: %w", lock.ErrLocked) }

func assertDeleteRefused(t *testing.T, err error, rec *revokeRecorder, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want a refusal mentioning %q", err, want)
	}
	if msg := err.Error(); strings.Contains(msg, "outcome unknown") || !(strings.Contains(msg, "not deleted") || strings.Contains(msg, "nothing was deleted")) {
		t.Fatalf("refusal %q must say that nothing was deleted", msg)
	}
	if calls := rec.calledWith(); len(calls) != 0 {
		t.Fatalf("native calls = %v, want none", calls)
	}
}

func assertDeleteUnknown(t *testing.T, err error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "delete outcome unknown") {
		t.Fatalf("error = %v, want an unknown outcome", err)
	}
	for _, claim := range []string{"not deleted", "nothing was deleted", "not sent"} {
		if strings.Contains(err.Error(), claim) {
			t.Fatalf("unknown outcome %q claims %q", err, claim)
		}
	}
}

func waitForDeleteState(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// newLockedDeleteStore stores the fixture message under its LID, maps the
// phone number to that LID in the session store, and holds the store lock as
// a running `sync --follow` process does.
func newLockedDeleteStore(t *testing.T) string {
	t.Helper()
	storeDir := shortPresenceDelegateStoreDir(t)
	db, err := store.Open(filepath.Join(storeDir, "wacli.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	putDeleteRow(t, db, delLID, delID, nil)
	putDeleteRow(t, db, delPN, "INBOUND1", fromSomeoneElse)
	if err := db.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	writeTestSessionLIDMap(t, filepath.Join(storeDir, "session.db"), "900000001", "15550001001")
	lk, err := lock.Acquire(storeDir)
	if err != nil {
		t.Fatalf("lock store: %v", err)
	}
	t.Cleanup(func() { _ = lk.Release() })
	return storeDir
}

// countDelegateConns listens on the store's delegate socket and returns a
// function that stops listening and reports how many connections arrived.
func countDelegateConns(t *testing.T, storeDir string) func() int32 {
	t.Helper()
	ln, err := delegate.Listen(storeDir)
	if err != nil {
		t.Fatalf("listen delegate socket: %v", err)
	}
	var accepted atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			_ = conn.Close()
		}
	}()
	var once sync.Once
	stop := func() int32 {
		once.Do(func() {
			_ = ln.Close()
			<-done
		})
		return accepted.Load()
	}
	t.Cleanup(func() { stop() })
	return stop
}

func TestDelegatedDeleteRevokesOnlyTheBoundRow(t *testing.T) {
	dir := t.TempDir()
	db := openDeleteStore(t, dir)
	putDeleteRow(t, db, delPN, delID, nil)
	// An adjacent ID, the same ID in another case, a prefix of it, and the
	// same ID in another chat.
	neighbours := [][2]string{{delPN, delNext}, {delPN, strings.ToLower(delID)}, {delPN, delID[:len(delID)-1]}, {delOther, delID}}
	for _, row := range neighbours {
		putDeleteRow(t, db, row[0], row[1], nil)
	}
	rec := &revokeRecorder{}
	resp, err := executeDelegatedDelete(context.Background(), testDelegatedDelete(dir, db, deleteNoAlias, rec), boundDelete(delPN, delPN, delID))
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !resp.OK || !resp.Sent || resp.To != delPN || resp.ID != "REVOKE-1" || resp.Target != delID || resp.StoreWarning != "" {
		t.Fatalf("response = %+v", resp)
	}
	if calls := rec.calledWith(); len(calls) != 1 || calls[0] != delPN+" "+delID {
		t.Fatalf("native calls = %v", calls)
	}
	if !revokedState(t, db, delPN, delID) {
		t.Fatal("bound row was not marked deleted")
	}
	for _, row := range neighbours {
		if revokedState(t, db, row[0], row[1]) {
			t.Fatalf("neighbouring row %s %s was marked deleted", row[0], row[1])
		}
	}
}

func TestDelegatedDeleteRefusalsMakeNoNativeCall(t *testing.T) {
	tests := []struct {
		name, to, target, id, want string
	}{
		{"missing ID", delPN, delPN, delNext, "not stored"},
		{"prefix of the ID", delPN, delPN, delID[:len(delID)-1], "not stored"},
		{"ID in another case", delPN, delPN, strings.ToLower(delID), "not stored"},
		{"ID with spaces", delPN, delPN, " " + delID, "exact message ID"},
		{"empty ID", delPN, delPN, "", "exact message ID"},
		{"name instead of a JID", "Fixture", delPN, delID, "resolved chat JID"},
		{"phone number instead of a JID", "+15550001001", delPN, delID, "resolved chat JID"},
		{"device JID", "15550001001:7@s.whatsapp.net", delPN, delID, "resolved chat JID"},
		{"unrelated chat", delOther, delOther, delID, "not stored"},
		{"other bound chat", delPN, delLID, delID, "not the bound chat"},
		{"no bound chat", delPN, "", delID, "not the bound chat"},
		{"message from someone else", delPN, delPN, "INBOUND1", "not sent by me"},
		{"already deleted", delPN, delPN, "GONE1", "already deleted"},
		{"deleted for me", delPN, delPN, "MINE1", "deleted for me"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			db := openDeleteStore(t, dir)
			putDeleteRow(t, db, delPN, delID, nil)
			putDeleteRow(t, db, delPN, "INBOUND1", fromSomeoneElse)
			putDeleteRow(t, db, delPN, "GONE1", func(p *store.UpsertMessageParams) { p.Revoked = true })
			putDeleteRow(t, db, delPN, "MINE1", func(p *store.UpsertMessageParams) { p.DeletedForMe = true })
			rec := &revokeRecorder{}
			_, err := executeDelegatedDelete(context.Background(), testDelegatedDelete(dir, db, deleteNoAlias, rec), boundDelete(tt.to, tt.target, tt.id))
			assertDeleteRefused(t, err, rec, tt.want)
			if revokedState(t, db, delPN, delID) {
				t.Fatal("refused delete changed the stored row")
			}
		})
	}
}

func TestDelegatedDeleteRefusesBeforeDispatch(t *testing.T) {
	dir := t.TempDir()
	db := openDeleteStore(t, dir)
	putDeleteRow(t, db, delPN, delID, nil)
	rec := &revokeRecorder{}
	d := testDelegatedDelete(dir, db, deleteNoAlias, rec)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := executeDelegatedDelete(canceled, d, boundDelete(delPN, delPN, delID))
	assertDeleteRefused(t, err, rec, "before dispatch")

	d.connected = func() bool { return false }
	_, err = executeDelegatedDelete(context.Background(), d, boundDelete(delPN, delPN, delID))
	assertDeleteRefused(t, err, rec, "not connected")
	if revokedState(t, db, delPN, delID) {
		t.Fatal("refused delete changed the stored row")
	}
}

func TestDelegatedDeleteUsesTheStoredMappingInBothDirections(t *testing.T) {
	for _, tt := range []struct {
		name, to, stored string
	}{
		{"phone number finds the LID row", delPN, delLID},
		{"LID finds the phone-number row", delLID, delPN},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			db := openDeleteStore(t, dir)
			putDeleteRow(t, db, tt.stored, delID, nil)
			rec := &revokeRecorder{}
			resp, err := executeDelegatedDelete(context.Background(), testDelegatedDelete(dir, db, deletePairAlias(delPN, delLID), rec), boundDelete(tt.to, tt.stored, delID))
			if err != nil {
				t.Fatalf("delete: %v", err)
			}
			if calls := rec.calledWith(); resp.To != tt.stored || len(calls) != 1 || calls[0] != tt.stored+" "+delID {
				t.Fatalf("response %+v, native calls %v", resp, calls)
			}
			if !revokedState(t, db, tt.stored, delID) {
				t.Fatal("stored row was not marked deleted")
			}
		})
	}
}

func TestDelegatedDeleteExactChatNeedsNoMapping(t *testing.T) {
	dir := t.TempDir()
	db := openDeleteStore(t, dir)
	putDeleteRow(t, db, delPN, delID, nil)
	putDeleteRow(t, db, delGroup, delID, nil)
	rec := &revokeRecorder{}
	d := testDelegatedDelete(dir, db, deleteNoAlias, rec)
	if _, err := executeDelegatedDelete(context.Background(), d, boundDelete(delPN, delPN, delID)); err != nil {
		t.Fatalf("user chat without a stored mapping: %v", err)
	}
	// A group has no phone-number/LID counterpart to look up.
	d.alias = func(context.Context, types.JID) (types.JID, error) {
		t.Error("mapping looked up for a group")
		return types.EmptyJID, nil
	}
	if _, err := executeDelegatedDelete(context.Background(), d, boundDelete(delGroup, delGroup, delID)); err != nil {
		t.Fatalf("group chat: %v", err)
	}
	if calls := rec.calledWith(); len(calls) != 2 {
		t.Fatalf("native calls = %v, want one per delete", calls)
	}
}

func TestDelegatedDeleteRefusesAnAliasRowWithoutAStoredMapping(t *testing.T) {
	dir := t.TempDir()
	db := openDeleteStore(t, dir)
	putDeleteRow(t, db, delLID, delID, nil)
	rec := &revokeRecorder{}
	_, err := executeDelegatedDelete(context.Background(), testDelegatedDelete(dir, db, deleteNoAlias, rec), boundDelete(delPN, delLID, delID))
	assertDeleteRefused(t, err, rec, "not stored")
}

func TestDelegatedDeleteRefusesARowStoredUnderBothIdentities(t *testing.T) {
	for _, tt := range []struct {
		name   string
		lidRow func(*store.UpsertMessageParams)
	}{
		{"identical rows", nil},
		{"conflicting own state", func(p *store.UpsertMessageParams) {
			p.FromMe = false
			p.SenderJID = delLID
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			db := openDeleteStore(t, dir)
			putDeleteRow(t, db, delPN, delID, nil)
			putDeleteRow(t, db, delLID, delID, tt.lidRow)
			for _, req := range []sendDelegateRequest{
				boundDelete(delPN, delPN, delID),
				boundDelete(delPN, delLID, delID),
				boundDelete(delLID, delLID, delID),
			} {
				rec := &revokeRecorder{}
				_, err := executeDelegatedDelete(context.Background(), testDelegatedDelete(dir, db, deletePairAlias(delPN, delLID), rec), req)
				assertDeleteRefused(t, err, rec, "more than one identity")
			}
		})
	}
}

// failingDeleteStore fails reads under one chat key, as a database error would.
type failingDeleteStore struct {
	deleteStore
	failChat string
}

func (s failingDeleteStore) GetMessage(chatJID, msgID string) (store.Message, error) {
	if chatJID == s.failChat {
		return store.Message{}, errors.New("disk I/O error")
	}
	return s.deleteStore.GetMessage(chatJID, msgID)
}

func TestDelegatedDeleteRefusesWhenALookupFails(t *testing.T) {
	dir := t.TempDir()
	db := openDeleteStore(t, dir)
	putDeleteRow(t, db, delPN, delID, nil)
	rec := &revokeRecorder{}
	mappingDown := func(context.Context, types.JID) (types.JID, error) {
		return types.EmptyJID, errors.New("session store unavailable")
	}
	_, err := executeDelegatedDelete(context.Background(), testDelegatedDelete(dir, db, mappingDown, rec), boundDelete(delPN, delPN, delID))
	assertDeleteRefused(t, err, rec, "session store unavailable")

	// A failed read is never taken as "no row under that identity".
	for _, failChat := range []string{delPN, delLID} {
		failing := failingDeleteStore{deleteStore: db, failChat: failChat}
		_, err = executeDelegatedDelete(context.Background(), testDelegatedDelete(dir, failing, deletePairAlias(delPN, delLID), rec), boundDelete(delPN, delPN, delID))
		assertDeleteRefused(t, err, rec, "disk I/O error")
	}
	if revokedState(t, db, delPN, delID) {
		t.Fatal("refused delete changed the stored row")
	}
}

func TestDelegatedDeleteRefusesWhenTheMappingChangedWhileQueued(t *testing.T) {
	const otherLID = "900000002@lid"
	dir := t.TempDir()
	db := openDeleteStore(t, dir)
	putDeleteRow(t, db, delLID, delID, nil)
	putDeleteRow(t, db, otherLID, delID, nil)
	// Bound while the phone number mapped to delLID.
	req := boundDelete(delPN, delLID, delID)
	for _, tt := range []struct {
		name  string
		alias chatAliasLookup
		want  string
	}{
		{"now mapped to another LID", deletePairAlias(delPN, otherLID), "not the bound chat"},
		{"mapping gone", deleteNoAlias, "not stored"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := &revokeRecorder{}
			_, err := executeDelegatedDelete(context.Background(), testDelegatedDelete(dir, db, tt.alias, rec), req)
			assertDeleteRefused(t, err, rec, tt.want)
		})
	}
	if revokedState(t, db, delLID, delID) || revokedState(t, db, otherLID, delID) {
		t.Fatal("a remapped delete changed a stored row")
	}
}

func TestDelegatedDeleteKeepsTheChatBoundBeforeQueueing(t *testing.T) {
	dir := t.TempDir()
	db := openDeleteStore(t, dir)
	putDeleteRow(t, db, delPN, delID, nil)
	putDeleteRow(t, db, delOther, delID, nil)
	rename := func(jid, name string) {
		t.Helper()
		if err := db.UpsertChat(jid, "dm", name, time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)); err != nil {
			t.Fatalf("rename chat: %v", err)
		}
	}
	rename(delPN, "Ana")
	flags := &rootFlags{storeDir: dir}
	req, err := bindDelegatedDelete(context.Background(), flags, "Ana", delID)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if req.Kind != deleteKind || req.To != delPN || req.TargetChat != delPN || req.ID != delID {
		t.Fatalf("bound request = %+v", req)
	}

	// While the request waits in the queue, the name moves to another chat.
	rename(delPN, "Former Ana")
	rename(delOther, "Ana")
	req.Version = sendDelegateVersion
	rec := &revokeRecorder{}
	if _, err := executeDelegatedDelete(context.Background(), testDelegatedDelete(dir, db, deleteNoAlias, rec), req); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if calls := rec.calledWith(); len(calls) != 1 || calls[0] != delPN+" "+delID {
		t.Fatalf("native calls = %v, want only the bound chat", calls)
	}
	if revokedState(t, db, delOther, delID) {
		t.Fatal("the renamed chat's message was deleted")
	}

	// A name two chats share never binds.
	rename(delPN, "Ana")
	if _, err := bindDelegatedDelete(context.Background(), flags, "Ana", delID); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous name bound: %v", err)
	}
}

func TestDelegatedDeleteNativeFailureIsUnknownAfterOneCall(t *testing.T) {
	for _, nativeErr := range []error{
		whatsmeow.ErrIQTimedOut,
		errors.New("failed to send usync query: websocket not connected"),
		errors.New("not connected"),
		context.DeadlineExceeded,
	} {
		t.Run(nativeErr.Error(), func(t *testing.T) {
			dir := t.TempDir()
			db := openDeleteStore(t, dir)
			putDeleteRow(t, db, delPN, delID, nil)
			rec := &revokeRecorder{err: nativeErr}
			_, err := executeDelegatedDelete(context.Background(), testDelegatedDelete(dir, db, deleteNoAlias, rec), boundDelete(delPN, delPN, delID))
			assertDeleteUnknown(t, err)
			if !errors.Is(err, nativeErr) {
				t.Fatalf("error %v lost its cause", err)
			}
			if calls := rec.calledWith(); len(calls) != 1 {
				t.Fatalf("native calls = %v, want exactly one with no retry", calls)
			}
			if revokedState(t, db, delPN, delID) {
				t.Fatal("unacknowledged delete was recorded locally")
			}
		})
	}
}

// failingTombstoneStore reads normally but cannot record the delete.
type failingTombstoneStore struct{ deleteStore }

func (failingTombstoneStore) MarkMessageRevoked(string, string) error {
	return errors.New("database is locked")
}

func TestDelegatedDeleteKeepsTheAcknowledgmentWhenTheTombstoneFails(t *testing.T) {
	dir := t.TempDir()
	db := openDeleteStore(t, dir)
	putDeleteRow(t, db, delPN, delID, nil)
	rec := &revokeRecorder{}
	resp, err := executeDelegatedDelete(context.Background(), testDelegatedDelete(dir, failingTombstoneStore{deleteStore: db}, deleteNoAlias, rec), boundDelete(delPN, delPN, delID))
	if err != nil {
		t.Fatalf("acknowledged delete reported as failed: %v", err)
	}
	if !resp.OK || !resp.Sent || resp.To != delPN || resp.ID != "REVOKE-1" || resp.Target != delID || !strings.Contains(resp.StoreWarning, "database is locked") {
		t.Fatalf("response = %+v", resp)
	}
	if calls := rec.calledWith(); len(calls) != 1 {
		t.Fatalf("native calls = %v", calls)
	}
}

func TestExecuteDelegatedSendRoutesDelete(t *testing.T) {
	dir := t.TempDir()
	a, err := app.New(app.Options{StoreDir: dir})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	defer a.Close()
	putDeleteRow(t, a.DB(), delGroup, delID, nil)
	putDeleteRow(t, a.DB(), delPN, delID, nil)

	// Without a session there is no connection and no stored mapping to
	// trust, so both stop before any native call.
	_, err = executeDelegatedSend(context.Background(), a, boundDelete(delGroup, delGroup, delID))
	if err == nil || !strings.Contains(err.Error(), "not connected") || !strings.Contains(err.Error(), "was not deleted") {
		t.Fatalf("group delete error = %v", err)
	}
	_, err = executeDelegatedSend(context.Background(), a, boundDelete(delPN, delPN, delID))
	if err == nil || !strings.Contains(err.Error(), "mapping is not available") || !strings.Contains(err.Error(), "was not deleted") {
		t.Fatalf("user chat delete error = %v", err)
	}
}

func TestDelegatedDeleteQueuedPastItsDeadlineIsRefusedUnsent(t *testing.T) {
	dir := t.TempDir()
	db := openDeleteStore(t, dir)
	putDeleteRow(t, db, delPN, delID, nil)
	rec := &revokeRecorder{}
	d := testDelegatedDelete(dir, db, deleteNoAlias, rec)
	execute := func(ctx context.Context, req sendDelegateRequest) (sendDelegateResponse, error) {
		return executeDelegatedDelete(ctx, d, req)
	}

	t.Run("send slot held by an earlier operation", func(t *testing.T) {
		serverConn, clientConn := net.Pipe()
		defer clientConn.Close()
		sendSlot := make(chan struct{}, 1) // empty: an earlier send owns it
		done := make(chan struct{})
		go func() {
			defer close(done)
			handleSendDelegateConn(context.Background(), serverConn, execute, sendSlot, newSendPacer(sendSpacing{}))
		}()
		req := boundDelete(delPN, delPN, delID)
		req.TimeoutMS = 20
		resp := roundTripDelegate(t, clientConn, req)
		<-done
		if resp.OK || !strings.Contains(resp.Error, "it was not sent") {
			t.Fatalf("response = %+v, want an explicit not-sent refusal", resp)
		}
	})

	t.Run("budget ran out before the native call", func(t *testing.T) {
		serverConn, clientConn := net.Pipe()
		defer clientConn.Close()
		sendSlot := make(chan struct{}, 1)
		sendSlot <- struct{}{}
		done := make(chan struct{})
		go func() {
			defer close(done)
			handleSendDelegateConn(context.Background(), serverConn, func(ctx context.Context, req sendDelegateRequest) (sendDelegateResponse, error) {
				<-ctx.Done()
				return execute(ctx, req)
			}, sendSlot, newSendPacer(sendSpacing{}))
		}()
		req := boundDelete(delPN, delPN, delID)
		req.TimeoutMS = 100
		resp := roundTripDelegate(t, clientConn, req)
		<-done
		// Nothing was sent, so the refusal is not reworded as possibly sent.
		if resp.OK || !strings.Contains(resp.Error, "was not deleted") || strings.Contains(resp.Error, "may still have gone through") {
			t.Fatalf("response = %+v, want the refusal as stated", resp)
		}
	})

	if calls := rec.calledWith(); len(calls) != 0 {
		t.Fatalf("native calls = %v, want none", calls)
	}
	if revokedState(t, db, delPN, delID) {
		t.Fatal("refused delete changed the stored row")
	}
}

func TestDelegatedDeleteThroughTheSendQueue(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	storeDir := shortPresenceDelegateStoreDir(t)
	db := openDeleteStore(t, storeDir)
	putDeleteRow(t, db, delPN, delID, nil)
	putDeleteRow(t, db, delPN, delNext, nil)
	rec := &revokeRecorder{}
	d := testDelegatedDelete(storeDir, db, deleteNoAlias, rec)
	stop, err := startSendDelegateServerForStore(context.Background(), storeDir, sendSpacing{}, func(ctx context.Context, req sendDelegateRequest) (sendDelegateResponse, error) {
		return executeDelegatedDelete(ctx, d, req)
	})
	if err != nil {
		t.Fatalf("start delegate server: %v", err)
	}
	defer stop()
	flags := &rootFlags{storeDir: storeDir, timeout: 2 * time.Second}

	// A post-send wait longer than the budget is cut short at the deadline;
	// the acknowledged delete is still reported as done.
	resp, delegated, err := tryDelegateDelete(context.Background(), flags, heldLockErr(), delPN, delID, time.Minute)
	if err != nil || !delegated || resp.To != delPN || resp.ID != "REVOKE-1" || resp.Target != delID {
		t.Fatalf("delete = %+v, delegated=%v, err=%v", resp, delegated, err)
	}
	if !revokedState(t, db, delPN, delID) {
		t.Fatal("acknowledged delete was not recorded locally")
	}

	// A native failure reaches the caller as unknown, after one call.
	rec.mu.Lock()
	rec.err = whatsmeow.ErrIQTimedOut
	rec.mu.Unlock()
	_, delegated, err = tryDelegateDelete(context.Background(), flags, heldLockErr(), delPN, delNext, 0)
	if !delegated {
		t.Fatalf("delete was not delegated: %v", err)
	}
	assertDeleteUnknown(t, err)
	if calls := rec.calledWith(); len(calls) != 2 {
		t.Fatalf("native calls = %v, want one per delete", calls)
	}
	if revokedState(t, db, delPN, delNext) {
		t.Fatal("unacknowledged delete was recorded locally")
	}
}

func TestDelegatedDeleteHeldPastItsDeadline(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	storeDir := shortPresenceDelegateStoreDir(t)
	db := openDeleteStore(t, storeDir)
	putDeleteRow(t, db, delPN, delID, nil)
	rec := &revokeRecorder{started: make(chan struct{}, 1), hold: make(chan struct{})}
	var release sync.Once
	finish := func() { release.Do(func() { close(rec.hold) }) }
	t.Cleanup(finish)
	d := testDelegatedDelete(storeDir, db, deleteNoAlias, rec)
	var texts atomic.Int32
	stop, err := startSendDelegateServerForStore(context.Background(), storeDir, sendSpacing{}, func(ctx context.Context, req sendDelegateRequest) (sendDelegateResponse, error) {
		if req.Kind == deleteKind {
			return executeDelegatedDelete(ctx, d, req)
		}
		texts.Add(1)
		return sendDelegateResponse{OK: true, Sent: true, ID: req.Message}, nil
	})
	if err != nil {
		t.Fatalf("start delegate server: %v", err)
	}
	defer stop()

	// The caller stops waiting while the native call is still running.
	deleted := make(chan error, 1)
	go func() {
		_, _, err := tryDelegateDelete(context.Background(), &rootFlags{storeDir: storeDir, timeout: 400 * time.Millisecond}, heldLockErr(), delPN, delID, 0)
		deleted <- err
	}()
	<-rec.started
	assertDeleteUnknown(t, <-deleted)

	// Intake keeps storing messages meanwhile.
	putDeleteRow(t, db, delPN, "INBOUND1", fromSomeoneElse)
	if _, err := db.GetMessage(delPN, "INBOUND1"); err != nil {
		t.Fatalf("inbound message not readable while the delete is held: %v", err)
	}

	// The send queue stays held: an ordinary send behind it is refused unsent.
	_, err = delegateSend(context.Background(), &rootFlags{storeDir: storeDir, timeout: sendDelegateReplyMargin + 300*time.Millisecond}, sendDelegateRequest{Kind: "text", Message: "queued"})
	if err == nil || !strings.Contains(err.Error(), "it was not sent") {
		t.Fatalf("queued send error = %v, want an explicit not-sent refusal", err)
	}
	if revokedState(t, db, delPN, delID) {
		t.Fatal("tombstone written before the native call returned")
	}

	// The late acknowledgment is still recorded locally.
	finish()
	waitForDeleteState(t, "the late tombstone", func() bool { return revokedState(t, db, delPN, delID) })

	// The next ordinary operation then runs.
	resp, err := delegateSend(context.Background(), &rootFlags{storeDir: storeDir, timeout: 2 * time.Second}, sendDelegateRequest{Kind: "text", Message: "next"})
	if err != nil || resp.ID != "next" {
		t.Fatalf("next send = %+v, %v", resp, err)
	}
	if n := texts.Load(); n != 1 {
		t.Fatalf("ordinary sends executed = %d, want only the next one", n)
	}
	if calls := rec.calledWith(); len(calls) != 1 {
		t.Fatalf("native calls = %v, want one", calls)
	}
}

func TestDelegatedDeleteLostOrMalformedReplyIsUnknown(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	reply := func(s string) func(net.Conn, sendDelegateRequest) {
		return func(conn net.Conn, _ sendDelegateRequest) { _, _ = conn.Write([]byte(s)) }
	}
	confirm := func(edit func(*sendDelegateResponse)) func(net.Conn, sendDelegateRequest) {
		return func(conn net.Conn, req sendDelegateRequest) {
			resp := sendDelegateResponse{OK: true, Sent: true, To: req.TargetChat, ID: "REVOKE-1", Target: req.ID}
			edit(&resp)
			_ = json.NewEncoder(conn).Encode(resp)
		}
	}
	for _, tt := range []struct {
		name    string
		respond func(net.Conn, sendDelegateRequest)
	}{
		{"connection closed without a reply", func(net.Conn, sendDelegateRequest) {}},
		{"partial reply", reply(`{"ok":true,"sent":tr`)},
		{"reply that is not JSON", reply("garbage\n")},
		{"failure without a reason", reply(`{"ok":false}` + "\n")},
		{"empty reply", reply("{}\n")},
		{"success without sent", confirm(func(r *sendDelegateResponse) { r.Sent = false })},
		{"success without an ID", confirm(func(r *sendDelegateResponse) { r.ID = "" })},
		{"success for another message", confirm(func(r *sendDelegateResponse) { r.Target = delNext })},
		{"success for another chat", confirm(func(r *sendDelegateResponse) { r.To = delOther })},
		{"no reply before the timeout", func(net.Conn, sendDelegateRequest) { time.Sleep(time.Second) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			storeDir := newLockedDeleteStore(t)
			ln, err := delegate.Listen(storeDir)
			if err != nil {
				t.Fatalf("listen delegate socket: %v", err)
			}
			t.Cleanup(func() { _ = ln.Close() })
			var recorded atomic.Int32
			go func() {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				var req sendDelegateRequest
				if err := json.NewDecoder(conn).Decode(&req); err != nil {
					return
				}
				// The fake native call is made before the reply goes wrong.
				recorded.Add(1)
				tt.respond(conn, req)
			}()
			flags := &rootFlags{storeDir: storeDir, timeout: 300 * time.Millisecond}
			_, delegated, err := tryDelegateDelete(context.Background(), flags, heldLockErr(), delPN, delID, 0)
			if !delegated {
				t.Fatalf("delete was not delegated: %v", err)
			}
			assertDeleteUnknown(t, err)
			if n := recorded.Load(); n != 1 {
				t.Fatalf("recorded calls = %d, want 1", n)
			}
		})
	}
}

func TestDelegatedDeleteRequestWriteFailureIsUnknown(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	storeDir := newLockedDeleteStore(t)
	ln, err := delegate.Listen(storeDir)
	if err != nil {
		t.Fatalf("listen delegate socket: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		_, _ = io.Copy(io.Discard, conn)
		_ = conn.Close()
	}()
	// A budget that has already run out fails the request write itself.
	flags := &rootFlags{storeDir: storeDir, timeout: time.Nanosecond}
	_, delegated, err := tryDelegateDelete(context.Background(), flags, heldLockErr(), delPN, delID, 0)
	if !delegated {
		t.Fatalf("delete was not delegated: %v", err)
	}
	assertDeleteUnknown(t, err)
	if !strings.Contains(err.Error(), "write") {
		t.Fatalf("error = %v, want the failed request write", err)
	}
}

func TestTryDelegateDeleteIgnoresOtherOpenErrors(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	storeDir := shortPresenceDelegateStoreDir(t)
	accepted := countDelegateConns(t, storeDir)
	openErr := errors.New("open sqlite: disk I/O error")
	_, delegated, err := tryDelegateDelete(context.Background(), &rootFlags{storeDir: storeDir, timeout: time.Second}, openErr, delPN, delID, 0)
	if delegated || !errors.Is(err, openErr) {
		t.Fatalf("delegated=%v err=%v, want the open error unchanged", delegated, err)
	}
	if n := accepted(); n != 0 {
		t.Fatalf("opened %d delegate connections for a non-lock error", n)
	}
}

func TestMessagesDeleteDelegatesTheBoundTargetWhenStoreLocked(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	for _, tt := range []struct {
		name         string
		storeWarning string
	}{
		{"recorded locally", ""},
		{"local store update failed", "store deleted message state: database is locked"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			storeDir := newLockedDeleteStore(t)
			server := startPresenceDelegateTestSocket(t, storeDir, func(req sendDelegateRequest) sendDelegateResponse {
				return sendDelegateResponse{OK: true, Sent: true, To: req.TargetChat, ID: "REVOKE-1", Target: req.ID, StoreWarning: tt.storeWarning}
			})
			defer server.stop()

			stdout, stderr, err := runPresenceDelegateHelper(t, []string{
				"--store", storeDir, "--json", "--timeout", "750ms",
				"messages", "delete", "--chat", "+1 555 000 1001", "--id", delID, "--post-send-wait", "25ms",
			})
			if err != nil {
				t.Fatalf("messages delete failed: %v stdout=%q stderr=%q", err, stdout, stderr)
			}
			req := server.nextRequest(t)
			if req.Version != sendDelegateVersion || req.Kind != deleteKind {
				t.Fatalf("delegate version/kind = %d/%q", req.Version, req.Kind)
			}
			// The phone number was bound to the row stored under its LID.
			if req.To != delPN || req.TargetChat != delLID || req.ID != delID {
				t.Fatalf("delegate target = %+v", req)
			}
			if req.TimeoutMS != 750 || req.PostSendWaitMS != 25 || req.DeadlineUnixMS == 0 {
				t.Fatalf("delegate budget = %+v", req)
			}
			if strings.Contains(stderr, "store is locked") {
				t.Fatalf("delegated command tried the direct store path: stderr=%q", stderr)
			}
			for _, want := range []string{`"revoked":true`, `"to":"` + delLID + `"`, `"id":"REVOKE-1"`, `"target":"` + delID + `"`} {
				if !strings.Contains(stdout, want) {
					t.Fatalf("stdout %q missing %s", stdout, want)
				}
			}
			if tt.storeWarning == "" {
				if strings.Contains(stdout, "store_warning") || strings.Contains(stderr, "local store update failed") {
					t.Fatalf("unexpected store warning: stdout=%q stderr=%q", stdout, stderr)
				}
				return
			}
			if !strings.Contains(stdout, `"store_warning":"`+tt.storeWarning+`"`) || !strings.Contains(stderr, "do not retry the delete") {
				t.Fatalf("store warning lost: stdout=%q stderr=%q", stdout, stderr)
			}
		})
	}
}

func TestMessagesDeleteRefusesBeforeContactingTheSyncProcess(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	for _, tt := range []struct {
		name, chat, id, want string
	}{
		{"message not stored", delPN, delNext, "is not stored"},
		{"ID with spaces", delPN, " " + delID, "exact message ID"},
		{"message from someone else", delPN, "INBOUND1", "not sent by me"},
		{"unknown name", "Nobody", delID, "no local exact name match"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			storeDir := newLockedDeleteStore(t)
			accepted := countDelegateConns(t, storeDir)
			stdout, stderr, err := runPresenceDelegateHelper(t, []string{"--store", storeDir, "--timeout", "750ms", "messages", "delete", "--chat", tt.chat, "--id", tt.id})
			if err == nil || !strings.Contains(stderr, tt.want) || strings.Contains(stderr, "outcome unknown") {
				t.Fatalf("err=%v stdout=%q stderr=%q, want a refusal mentioning %q", err, stdout, stderr, tt.want)
			}
			if n := accepted(); n != 0 {
				t.Fatalf("refused delete opened %d delegate connections", n)
			}
		})
	}
}

func TestMessagesDeleteFallsBackOnlyToTheLockError(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	args := func(storeDir string, extra ...string) []string {
		return append([]string{"--store", storeDir, "--timeout", "750ms", "messages", "delete", "--chat", delLID, "--id", delID}, extra...)
	}

	t.Run("no sync process", func(t *testing.T) {
		storeDir := newLockedDeleteStore(t)
		stdout, stderr, err := runPresenceDelegateHelper(t, args(storeDir))
		if err == nil || stdout != "" || !strings.Contains(stderr, "store is locked") || strings.Contains(stderr, "send delegate unavailable") {
			t.Fatalf("err=%v stdout=%q stderr=%q, want the original lock error", err, stdout, stderr)
		}
	})

	t.Run("sync process without delete support", func(t *testing.T) {
		storeDir := newLockedDeleteStore(t)
		server := startPresenceDelegateTestSocket(t, storeDir, func(sendDelegateRequest) sendDelegateResponse {
			// What a daemon from before this change answers.
			return sendDelegateResponse{OK: false, Error: `unsupported send kind "delete"`}
		})
		defer server.stop()
		stdout, stderr, err := runPresenceDelegateHelper(t, args(storeDir))
		_ = server.nextRequest(t)
		if err == nil || stdout != "" || !strings.Contains(stderr, `unsupported send kind "delete"`) || strings.Contains(stderr, "outcome unknown") || strings.Contains(stderr, "store is locked") {
			t.Fatalf("err=%v stdout=%q stderr=%q, want the daemon's refusal", err, stdout, stderr)
		}
	})

	t.Run("delete for me", func(t *testing.T) {
		storeDir := newLockedDeleteStore(t)
		accepted := countDelegateConns(t, storeDir)
		stdout, stderr, err := runPresenceDelegateHelper(t, args(storeDir, "--for-me"))
		if err == nil || !strings.Contains(stderr, "store is locked") {
			t.Fatalf("err=%v stdout=%q stderr=%q, want the lock error", err, stdout, stderr)
		}
		if n := accepted(); n != 0 {
			t.Fatalf("delete for me opened %d delegate connections", n)
		}
	})
}

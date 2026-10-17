package app

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/types"
)

var (
	testContactPN  = types.NewJID("15550001111", types.DefaultUserServer)
	testContactLID = types.NewJID("987654321098765", types.HiddenUserServer)
)

func newContactSaveTestApp(t *testing.T) (*App, *fakeWA) {
	t.Helper()
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	f.lids[testContactLID] = testContactPN
	return a, f
}

type storedContactNames struct {
	full, first, push, business string
	found                       bool
}

// storedContact reads the contact row after every admitted persistence task.
func storedContact(t *testing.T, a *App, jid types.JID) storedContactNames {
	t.Helper()
	if err := a.appStatePersist.waitIdle(t.Context()); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite3", filepath.Join(a.StoreDir(), "wacli.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var full, first, push, business sql.NullString
	err = raw.QueryRow(`SELECT full_name, first_name, push_name, business_name FROM contacts WHERE jid = ?`, jid.String()).
		Scan(&full, &first, &push, &business)
	if errors.Is(err, sql.ErrNoRows) {
		return storedContactNames{}
	}
	if err != nil {
		t.Fatal(err)
	}
	return storedContactNames{full: full.String, first: first.String, push: push.String, business: business.String, found: true}
}

func TestSaveContactSendsLIDAndMirrorsDerivedFirstName(t *testing.T) {
	a, f := newContactSaveTestApp(t)
	if err := a.db.UpsertContact(testContactPN.String(), testContactPN.User, "Push", "Old Name", "Old", "Biz"); err != nil {
		t.Fatal(err)
	}
	saved, err := a.SaveContact(t.Context(), testContactPN, "Ada Lovelace", "", true)
	if err != nil {
		t.Fatal(err)
	}
	want := SavedContact{JID: testContactPN, LID: testContactLID, FullName: "Ada Lovelace", FirstName: "Ada", Phonebook: true}
	if saved != want {
		t.Fatalf("saved = %+v, want %+v", saved, want)
	}
	if len(f.saveContactCalls) != 1 || f.saveContactCalls[0] != (fakeSaveContactCall{
		target: testContactPN, lid: testContactLID, fullName: "Ada Lovelace", firstName: "Ada", phonebook: true,
	}) {
		t.Fatalf("calls = %+v", f.saveContactCalls)
	}
	if got := storedContact(t, a, testContactPN); got != (storedContactNames{full: "Ada Lovelace", first: "Ada", push: "Push", business: "Biz", found: true}) {
		t.Fatalf("local mirror = %+v", got)
	}
}

func TestSaveContactRefusesWithoutLID(t *testing.T) {
	a, f := newContactSaveTestApp(t)
	other := types.NewJID("15550002222", types.DefaultUserServer)
	_, err := a.SaveContact(t.Context(), other, "Ada Lovelace", "", false)
	if err == nil || !strings.Contains(err.Error(), "LID") || !strings.Contains(err.Error(), "nothing was saved") {
		t.Fatalf("err = %v", err)
	}
	if len(f.saveContactCalls) != 0 {
		t.Fatalf("mutation sent without a LID: %+v", f.saveContactCalls)
	}
	if got := storedContact(t, a, other); got.found {
		t.Fatalf("local index updated although nothing was saved: %+v", got)
	}
}

func TestSaveContactSendErrorLeavesLocalIndexAlone(t *testing.T) {
	a, f := newContactSaveTestApp(t)
	f.saveContactErr = errors.New("boom")
	_, err := a.SaveContact(t.Context(), testContactPN, "Ada Lovelace", "", false)
	if err == nil || !strings.Contains(err.Error(), "save contact: boom") {
		t.Fatalf("err = %v", err)
	}
	if got := storedContact(t, a, testContactPN); got.found {
		t.Fatalf("local index updated after a failed send: %+v", got)
	}
}

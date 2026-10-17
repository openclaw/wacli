package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/openclaw/wacli/internal/app"
	"go.mau.fi/whatsmeow/types"
)

func TestParseContactSaveTarget(t *testing.T) {
	got, err := parseContactSaveTarget("+55 11 99999-0000")
	if err != nil || got.String() != "5511999990000@s.whatsapp.net" {
		t.Fatalf("got %v, %v", got, err)
	}
	if _, err := parseContactSaveTarget("123@lid"); err == nil {
		t.Fatal("LID accepted")
	}
	if _, err := parseContactSaveTarget("120363000000@g.us"); err == nil {
		t.Fatal("group accepted")
	}
	if _, err := parseContactSaveTarget("  "); err == nil {
		t.Fatal("empty accepted")
	}
	if got, err := parseContactSaveTarget("5511999990000:3@s.whatsapp.net"); err != nil || got.String() != "5511999990000@s.whatsapp.net" {
		t.Fatalf("device JID: got %v, %v", got, err)
	}
	for _, raw := range []string{"@s.whatsapp.net", "abc@s.whatsapp.net", "+55 11@s.whatsapp.net"} {
		if _, err := parseContactSaveTarget(raw); err == nil {
			t.Fatalf("malformed phone JID %q accepted", raw)
		}
	}
}

func TestContactsSaveRequiresName(t *testing.T) {
	cmd := newContactsSaveCmd(&rootFlags{})
	cmd.SetArgs([]string{"--jid", "5511999990000"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.Execute(); err == nil || err.Error() != "--name is required" {
		t.Fatalf("err = %v", err)
	}
}

// Names are checked before the store is opened or a daemon is asked, so the
// empty rootFlags here would fail loudly if validation ran any later.
func TestContactsSaveRejectsControlCharactersBeforeConnecting(t *testing.T) {
	for _, args := range [][]string{
		{"--jid", "5511999990000", "--name", "Maria\nSouza"},
		{"--jid", "5511999990000", "--name", "Maria", "--first-name", "Ma\x00ria"},
		{"--jid", "5511999990000", "--name", "Maria\tSouza"},
	} {
		cmd := newContactsSaveCmd(&rootFlags{})
		cmd.SetArgs(args)
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "control characters") {
			t.Fatalf("args %q: err = %v", args, err)
		}
	}
}

// fakeContactSaver stands in for the follow process's app.
type fakeContactSaver struct {
	err   error
	saved []string
}

func (f *fakeContactSaver) SaveContact(_ context.Context, target types.JID, fullName, firstName string, phonebook bool) (app.SavedContact, error) {
	f.saved = append(f.saved, fmt.Sprintf("%s %q %q %v", target, fullName, firstName, phonebook))
	if f.err != nil {
		return app.SavedContact{}, f.err
	}
	if firstName == "" {
		firstName = strings.Fields(fullName)[0]
	}
	return app.SavedContact{
		JID: target, LID: types.NewJID("99887766", types.HiddenUserServer),
		FullName: fullName, FirstName: firstName, Phonebook: phonebook,
	}, nil
}

func TestExecuteDelegatedContactsSaveRunsTheAppSave(t *testing.T) {
	saver := &fakeContactSaver{}
	resp, err := executeDelegatedContactsSave(context.Background(), saver, sendDelegateRequest{
		Kind: contactsSaveKind, To: "+1 555 000 1111", Name: "  Ada Lovelace ", Phonebook: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{`15550001111@s.whatsapp.net "Ada Lovelace" "" true`}; !slices.Equal(saver.saved, want) {
		t.Fatalf("saved = %q, want %q", saver.saved, want)
	}
	if !resp.OK || resp.Contact == nil {
		t.Fatalf("response = %+v", resp)
	}
	got := *resp.Contact
	want := contactSaveResult{JID: "15550001111@s.whatsapp.net", Name: "Ada Lovelace", FirstName: "Ada", Phonebook: true, LID: "99887766@lid"}
	if got != want {
		t.Fatalf("contact = %+v, want %+v", got, want)
	}
}

func TestExecuteDelegatedContactsSaveValidatesLikeTheCommand(t *testing.T) {
	saver := &fakeContactSaver{}
	for _, req := range []sendDelegateRequest{
		{Kind: contactsSaveKind, To: "123@lid", Name: "Ada"},
		{Kind: contactsSaveKind, To: "15550001111", Name: "   "},
		{Kind: contactsSaveKind, Name: "Ada"},
	} {
		if _, err := executeDelegatedContactsSave(context.Background(), saver, req); err == nil {
			t.Errorf("request %+v accepted", req)
		}
	}
	if saver.saved != nil {
		t.Fatalf("invalid requests reached the session: %q", saver.saved)
	}
}

func TestExecuteDelegatedContactsSaveReportsSaveErrors(t *testing.T) {
	saver := &fakeContactSaver{err: errors.New("save contact: boom")}
	_, err := executeDelegatedContactsSave(context.Background(), saver, sendDelegateRequest{
		Kind: contactsSaveKind, To: "15550001111", Name: "Ada",
	})
	if err == nil || !strings.Contains(err.Error(), "save contact: boom") {
		t.Fatalf("err = %v", err)
	}
}

func TestExplainContactsSaveDelegateErrorNamesRestartForOlderDaemons(t *testing.T) {
	old := errors.New(`unsupported send kind "contacts_save"`)
	if err := explainContactsSaveDelegateError(old); err == nil || !strings.Contains(err.Error(), "restart `wacli sync`") || !errors.Is(err, old) {
		t.Fatalf("error = %v, want restart hint wrapping the original", err)
	}
	other := errors.New("not connected")
	if err := explainContactsSaveDelegateError(other); err != other {
		t.Fatalf("error = %v, want unrelated error unchanged", err)
	}
}

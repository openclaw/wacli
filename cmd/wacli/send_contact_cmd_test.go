package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"
)

func TestBuildContactVCard(t *testing.T) {
	got := buildContactVCard("Jane Doe", []string{"15550100", "15550101"}, "Example Inc")
	want := strings.Join([]string{
		"BEGIN:VCARD",
		"VERSION:3.0",
		"N:;Jane Doe;;;",
		"FN:Jane Doe",
		"ORG:Example Inc",
		"TEL;type=CELL;waid=15550100:+15550100",
		"TEL;type=CELL;waid=15550101:+15550101",
		"END:VCARD",
	}, "\n")
	if got != want {
		t.Fatalf("vCard mismatch\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestBuildContactVCardEscapesTextAndOmitsEmptyOrg(t *testing.T) {
	got := buildContactVCard("Doe, Jane; Jr\\\nSecond", []string{"15550100"}, "")
	if strings.Contains(got, "ORG:") {
		t.Fatalf("expected no ORG line, got:\n%s", got)
	}
	if !strings.Contains(got, "\nFN:Doe\\, Jane\\; Jr\\\\\\nSecond\n") {
		t.Fatalf("expected escaped FN line, got:\n%s", got)
	}
	if strings.Count(got, "\n") != 5 {
		t.Fatalf("a newline in the name must not add a vCard line, got:\n%s", got)
	}
}

func TestResolveContactOptionsBuildsCard(t *testing.T) {
	opts, err := resolveContactOptions(contactCardInput{
		Name:   "  Jane Doe  ",
		Phones: []string{"+1 (555) 010-0100", "15550101"},
		Org:    " Example Inc ",
	}, "", false)
	if err != nil {
		t.Fatalf("resolveContactOptions: %v", err)
	}
	if opts.DisplayName != "Jane Doe" {
		t.Fatalf("display name = %q want %q", opts.DisplayName, "Jane Doe")
	}
	for _, line := range []string{
		"TEL;type=CELL;waid=15550100100:+15550100100",
		"TEL;type=CELL;waid=15550101:+15550101",
		"ORG:Example Inc",
	} {
		if !strings.Contains(opts.VCard, line+"\n") {
			t.Fatalf("expected %q in vCard:\n%s", line, opts.VCard)
		}
	}
}

func TestResolveContactOptionsValidation(t *testing.T) {
	tests := []struct {
		name      string
		input     contactCardInput
		raw       string
		useRaw    bool
		wantError string
	}{
		{name: "missing name", input: contactCardInput{Phones: []string{"+15550100"}}, wantError: "--name is required"},
		{name: "blank name", input: contactCardInput{Name: "   ", Phones: []string{"+15550100"}}, wantError: "--name is required"},
		{name: "missing phone", input: contactCardInput{Name: "Jane Doe"}, wantError: "at least one --phone is required"},
		{name: "empty phone", input: contactCardInput{Name: "Jane Doe", Phones: []string{" "}}, wantError: "--phone must not be empty"},
		{name: "letters in phone", input: contactCardInput{Name: "Jane Doe", Phones: []string{"+1 555 CALL"}}, wantError: "invalid --phone"},
		{name: "too short phone", input: contactCardInput{Name: "Jane Doe", Phones: []string{"12345"}}, wantError: "must be 7-15 digits"},
		{name: "too long phone", input: contactCardInput{Name: "Jane Doe", Phones: []string{"+1234567890123456"}}, wantError: "must be 7-15 digits"},
		{name: "JID phone", input: contactCardInput{Name: "Jane Doe", Phones: []string{"15550100@s.whatsapp.net"}}, wantError: "not a JID"},
		{name: "empty raw vCard", useRaw: true, raw: " \n", wantError: "--vcard is empty"},
		{name: "not a vCard", useRaw: true, raw: "hello", wantError: "BEGIN:VCARD to END:VCARD"},
		{name: "unterminated vCard", useRaw: true, raw: "BEGIN:VCARD\nFN:Jane Doe\n", wantError: "BEGIN:VCARD to END:VCARD"},
		{name: "two vCards", useRaw: true, raw: "BEGIN:VCARD\nFN:A\nEND:VCARD\nBEGIN:VCARD\nFN:B\nEND:VCARD", wantError: "exactly one card"},
		{name: "raw vCard without FN or name", useRaw: true, raw: "BEGIN:VCARD\nVERSION:3.0\nTEL:+15550100\nEND:VCARD", wantError: "--name is required when the vCard has no FN"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveContactOptions(tc.input, tc.raw, tc.useRaw)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantError)
			}
			if !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("expected error containing %q, got %q", tc.wantError, err.Error())
			}
		})
	}
}

func TestResolveContactOptionsRawVCard(t *testing.T) {
	raw := "\ufeffBEGIN:VCARD\r\nVERSION:3.0\r\nFN:Jane Doe\r\nTEL;type=CELL;waid=15550100:+15550100\r\nEND:VCARD\r\n"

	opts, err := resolveContactOptions(contactCardInput{}, raw, true)
	if err != nil {
		t.Fatalf("resolveContactOptions: %v", err)
	}
	if opts.DisplayName != "Jane Doe" {
		t.Fatalf("display name from FN = %q want %q", opts.DisplayName, "Jane Doe")
	}
	if !strings.HasPrefix(opts.VCard, "BEGIN:VCARD\r\n") || !strings.HasSuffix(opts.VCard, "END:VCARD") {
		t.Fatalf("expected the vCard as-is minus BOM and outer whitespace, got %q", opts.VCard)
	}

	opts, err = resolveContactOptions(contactCardInput{Name: "Jane"}, raw, true)
	if err != nil {
		t.Fatalf("resolveContactOptions with --name: %v", err)
	}
	if opts.DisplayName != "Jane" {
		t.Fatalf("--name should override FN, got %q", opts.DisplayName)
	}
}

func TestReadContactVCard(t *testing.T) {
	card := "BEGIN:VCARD\nFN:Jane Doe\nEND:VCARD\n"
	path := filepath.Join(t.TempDir(), "jane.vcf")
	if err := os.WriteFile(path, []byte(card), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readContactVCard(path, nil)
	if err != nil || got != card {
		t.Fatalf("file read = %q, %v", got, err)
	}
	got, err = readContactVCard("-", strings.NewReader(card))
	if err != nil || got != card {
		t.Fatalf("stdin read = %q, %v", got, err)
	}
	_, err = readContactVCard("-", strings.NewReader(strings.Repeat("x", maxContactVCardBytes+1)))
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("expected a size error for oversized stdin, got %v", err)
	}
}

func TestSendContactCmdRejectsVCardWithBuiltCardFlags(t *testing.T) {
	cmd := newSendContactCmd(&rootFlags{})
	cmd.SetArgs([]string{"--to", "15551234567", "--vcard", "-", "--phone", "+15550100"})
	cmd.SetOut(new(strings.Builder))
	cmd.SetErr(new(strings.Builder))
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--vcard cannot be combined") {
		t.Fatalf("expected a flag conflict error, got %v", err)
	}
}

func TestSendContactMessageSendsProtoToRecipient(t *testing.T) {
	sender := &recordingTextSender{nextProtoMsgID: "contact-id"}
	to := types.JID{User: "15551234567", Server: types.DefaultUserServer}
	opts := contactOptions{DisplayName: "Jane Doe", VCard: buildContactVCard("Jane Doe", []string{"15550100"}, "")}

	id, err := sendContactMessage(context.Background(), sender, to, opts)
	if err != nil {
		t.Fatalf("sendContactMessage: %v", err)
	}
	if id != "contact-id" || sender.protoCalls != 1 || sender.textCalls != 0 {
		t.Fatalf("id=%q protoCalls=%d textCalls=%d", id, sender.protoCalls, sender.textCalls)
	}
	if sender.protoRecipient != to {
		t.Fatalf("recipient = %v want %v", sender.protoRecipient, to)
	}
	contact := sender.protoMsg.GetContactMessage()
	if contact.GetDisplayName() != "Jane Doe" || contact.GetVcard() != opts.VCard {
		t.Fatalf("wire contact = %q / %q", contact.GetDisplayName(), contact.GetVcard())
	}
}

func TestPersistOutboundContactCanonicalizesChatAndMatchesSyncText(t *testing.T) {
	db := openSendTestDB(t)
	lid := types.JID{User: "999123456789", Server: types.HiddenUserServer}
	pn := types.JID{User: "15551234567", Server: types.DefaultUserServer}
	resolver := outboundTextResolverStub{lid: lid, pn: pn}
	opts := contactOptions{DisplayName: "Jane Doe", VCard: buildContactVCard("Jane Doe", []string{"15550100"}, "")}

	if err := persistOutboundContactWith(context.Background(), db, resolver, lid, "contact-1", opts, time.Now().UTC()); err != nil {
		t.Fatalf("persistOutboundContactWith: %v", err)
	}

	msg, err := db.GetMessage(pn.String(), "contact-1")
	if err != nil {
		t.Fatalf("GetMessage under the phone JID: %v", err)
	}
	if !msg.FromMe {
		t.Fatal("expected the stored contact to be outbound")
	}
	const want = "Contact: Jane Doe (+15550100)"
	if msg.Text != want || msg.DisplayText != want {
		t.Fatalf("text = %q display = %q want %q", msg.Text, msg.DisplayText, want)
	}
}

func TestExecuteDelegatedSendRejectsInvalidContact(t *testing.T) {
	_, err := executeDelegatedSend(context.Background(), nil, sendDelegateRequest{
		Version: sendDelegateVersion,
		Kind:    "contact",
		To:      "15551234567",
		Name:    "Jane Doe",
		VCard:   "not a card",
	})
	if err == nil || !strings.Contains(err.Error(), "BEGIN:VCARD") {
		t.Fatalf("expected an invalid vCard to be rejected before any send, got %v", err)
	}
}

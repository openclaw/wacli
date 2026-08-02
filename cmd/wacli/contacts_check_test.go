package main

import (
	"context"
	"errors"
	"testing"

	"go.mau.fi/whatsmeow/types"
)

type fakeRegistrationChecker struct {
	gotPhones []string
	resp      []types.IsOnWhatsAppResponse
	err       error
}

func (f *fakeRegistrationChecker) IsOnWhatsApp(_ context.Context, phones []string) ([]types.IsOnWhatsAppResponse, error) {
	f.gotPhones = append([]string(nil), phones...)
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func TestCheckRegistrationsNormalizesAndMaps(t *testing.T) {
	jid := types.NewJID("4366412345678", types.DefaultUserServer)
	checker := &fakeRegistrationChecker{
		resp: []types.IsOnWhatsAppResponse{
			{Query: "+4366412345678", JID: jid, IsIn: true},
			{Query: "+15550000001", IsIn: false},
		},
	}
	results, err := checkRegistrations(context.Background(), checker, []string{"+43 664 12345678", "1 (555) 000-0001"})
	if err != nil {
		t.Fatalf("checkRegistrations: %v", err)
	}
	if len(checker.gotPhones) != 2 || checker.gotPhones[0] != "+4366412345678" || checker.gotPhones[1] != "+15550000001" {
		t.Fatalf("unexpected phones: %#v", checker.gotPhones)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if !results[0].Registered || results[0].JID != jid.String() {
		t.Fatalf("expected registered JID %s, got %+v", jid, results[0])
	}
	if results[1].Registered || results[1].JID != "" {
		t.Fatalf("expected unregistered, got %+v", results[1])
	}
	if results[0].Phone != "4366412345678" || results[1].Phone != "15550000001" {
		t.Fatalf("unexpected phones: %+v", results)
	}
}

func TestCheckRegistrationsDeduplicatesLookups(t *testing.T) {
	jid := types.NewJID("4366412345678", types.DefaultUserServer)
	checker := &fakeRegistrationChecker{
		resp: []types.IsOnWhatsAppResponse{
			{Query: "+4366412345678", JID: jid, IsIn: true},
		},
	}
	results, err := checkRegistrations(context.Background(), checker, []string{"+43 664 12345678", "4366412345678@s.whatsapp.net"})
	if err != nil {
		t.Fatalf("checkRegistrations: %v", err)
	}
	if len(checker.gotPhones) != 1 || checker.gotPhones[0] != "+4366412345678" {
		t.Fatalf("expected single deduplicated lookup, got %#v", checker.gotPhones)
	}
	if len(results) != 2 {
		t.Fatalf("expected per-input results, got %d", len(results))
	}
	for _, r := range results {
		if !r.Registered || r.JID != jid.String() {
			t.Fatalf("expected both inputs registered as %s, got %+v", jid, r)
		}
	}
}

func TestCheckRegistrationsRejectsNonUserJID(t *testing.T) {
	checker := &fakeRegistrationChecker{}
	_, err := checkRegistrations(context.Background(), checker, []string{"12345@g.us"})
	if err == nil {
		t.Fatal("expected error for group JID")
	}
	if len(checker.gotPhones) != 0 {
		t.Fatalf("checker should not be called, got %#v", checker.gotPhones)
	}
}

func TestCheckRegistrationsPropagatesError(t *testing.T) {
	checker := &fakeRegistrationChecker{err: errors.New("boom")}
	_, err := checkRegistrations(context.Background(), checker, []string{"+4366412345678"})
	if err == nil || err.Error() != "boom" {
		t.Fatalf("expected boom, got %v", err)
	}
}

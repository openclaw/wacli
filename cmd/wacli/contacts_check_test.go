package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/openclaw/wacli/internal/lock"
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
	if !results[0].Responded || !results[1].Responded {
		t.Fatalf("expected both marked responded, got %+v", results)
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

func TestCheckRegistrationsMarksOmittedResponses(t *testing.T) {
	checker := &fakeRegistrationChecker{
		resp: []types.IsOnWhatsAppResponse{
			{Query: "+15550000001", IsIn: false},
		},
	}
	results, err := checkRegistrations(context.Background(), checker, []string{"+4366412345678", "+15550000001"})
	if err != nil {
		t.Fatalf("checkRegistrations: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Responded || results[0].Registered {
		t.Fatalf("expected omitted number to stay unresponded, got %+v", results[0])
	}
	if !results[1].Responded || results[1].Registered {
		t.Fatalf("expected confirmed negative, got %+v", results[1])
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

func TestExecuteDelegatedContactsCheckReturnsPerQueryResults(t *testing.T) {
	jid := types.NewJID("4366412345678", types.DefaultUserServer)
	checker := &fakeRegistrationChecker{
		resp: []types.IsOnWhatsAppResponse{{Query: "+4366412345678", JID: jid, IsIn: true}},
	}
	resp, err := executeDelegatedContactsCheck(context.Background(), checker, sendDelegateRequest{
		Kind:   contactsCheckKind,
		Phones: []string{"+43 664 12345678", "+15550000001"},
	})
	if err != nil {
		t.Fatalf("executeDelegatedContactsCheck: %v", err)
	}
	if !resp.OK || len(resp.Contacts) != 2 {
		t.Fatalf("response = %+v, want ok with two results", resp)
	}
	if !resp.Contacts[0].Registered || resp.Contacts[0].JID != jid.String() {
		t.Fatalf("first result = %+v, want registered %s", resp.Contacts[0], jid)
	}
	if resp.Contacts[1].Responded {
		t.Fatalf("second result = %+v, want no response", resp.Contacts[1])
	}
}

func TestExecuteDelegatedContactsCheckRequiresPhones(t *testing.T) {
	checker := &fakeRegistrationChecker{}
	_, err := executeDelegatedContactsCheck(context.Background(), checker, sendDelegateRequest{Kind: contactsCheckKind})
	if err == nil || !strings.Contains(err.Error(), "at least one phone") {
		t.Fatalf("error = %v, want phone validation", err)
	}
	if checker.gotPhones != nil {
		t.Fatalf("checker called with %#v, want no lookup", checker.gotPhones)
	}
}

func TestExplainContactsCheckDelegateErrorNamesRestartForOlderDaemons(t *testing.T) {
	old := errors.New(`unsupported send kind "contacts_check"`)
	if err := explainContactsCheckDelegateError(old); err == nil || !strings.Contains(err.Error(), "restart `wacli sync`") || !errors.Is(err, old) {
		t.Fatalf("error = %v, want restart hint wrapping the original", err)
	}
	other := errors.New("usync timeout")
	if err := explainContactsCheckDelegateError(other); err != other {
		t.Fatalf("error = %v, want unrelated error unchanged", err)
	}
}

func TestContactsCheckDelegatesThroughProductionServerWhenStoreLocked(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	storeDir := shortPresenceDelegateStoreDir(t)
	lk, err := lock.Acquire(storeDir)
	if err != nil {
		t.Fatalf("lock store: %v", err)
	}
	defer lk.Release()

	jid := types.NewJID("4366412345678", types.DefaultUserServer)
	checker := &fakeRegistrationChecker{
		resp: []types.IsOnWhatsAppResponse{{Query: "+4366412345678", JID: jid, IsIn: true}},
	}
	stop, err := startSendDelegateServerForStore(context.Background(), storeDir, sendSpacing{}, func(ctx context.Context, req sendDelegateRequest) (sendDelegateResponse, error) {
		if req.Version != sendDelegateVersion || req.Kind != contactsCheckKind {
			return sendDelegateResponse{}, fmt.Errorf("unexpected delegated request %d/%q", req.Version, req.Kind)
		}
		return executeDelegatedContactsCheck(ctx, checker, req)
	})
	if err != nil {
		t.Fatalf("start production delegate server: %v", err)
	}
	defer stop()

	stdout, stderr, err := runPresenceDelegateHelper(t, []string{
		"--store", storeDir, "--json", "--timeout", "750ms",
		"contacts", "check", "+43 664 12345678",
	})
	if err != nil {
		t.Fatalf("contacts check failed: %v stdout=%q stderr=%q", err, stdout, stderr)
	}
	if strings.Contains(stderr, "store is locked") {
		t.Fatalf("delegated command returned lock error: stderr=%q", stderr)
	}
	if len(checker.gotPhones) != 1 || checker.gotPhones[0] != "+4366412345678" {
		t.Fatalf("delegated lookup phones = %#v, want +4366412345678", checker.gotPhones)
	}
	for _, want := range []string{`"query":"+43 664 12345678"`, `"registered":true`, `"responded":true`, `"jid":"` + jid.String() + `"`} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout %q missing %s", stdout, want)
		}
	}
}

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"go.mau.fi/whatsmeow/types"
)

func TestGroupsAdminCommandsRegistered(t *testing.T) {
	cmd := newGroupsCmd(&rootFlags{})
	for _, name := range []string{"create", "topic", "description", "announce-only", "locked", "requests"} {
		if got, _, err := cmd.Find([]string{name}); err != nil || got == nil || got.Name() != name {
			t.Fatalf("groups command %q not registered: got=%v err=%v", name, got, err)
		}
	}
}

func TestParseGroupJIDRejectsNonGroup(t *testing.T) {
	_, err := parseGroupJID("123@s.whatsapp.net")
	if err == nil || !strings.Contains(err.Error(), "@g.us") {
		t.Fatalf("parseGroupJID error = %v, want group JID rejection", err)
	}

	jid, err := parseGroupJID("123@g.us")
	if err != nil {
		t.Fatalf("parseGroupJID: %v", err)
	}
	if jid.Server != types.GroupServer {
		t.Fatalf("server = %q, want %q", jid.Server, types.GroupServer)
	}
}

func TestParseGroupUserJIDsAcceptsPhonesAndJIDs(t *testing.T) {
	jids, err := parseGroupUserJIDs([]string{"+1 (234) 567-8900", "111@s.whatsapp.net"})
	if err != nil {
		t.Fatalf("parseGroupUserJIDs: %v", err)
	}
	if len(jids) != 2 || jids[0].User != "12345678900" || jids[1].String() != "111@s.whatsapp.net" {
		t.Fatalf("unexpected JIDs: %+v", jids)
	}
}

func TestParseOnOffFlagsRequiresExactlyOneMode(t *testing.T) {
	cmd := toggleTestCmd()
	if _, err := parseOnOffFlags(cmd, false, false); err == nil {
		t.Fatal("expected missing toggle error")
	}

	cmd = toggleTestCmd()
	if err := cmd.Flags().Set("on", "true"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("off", "true"); err != nil {
		t.Fatal(err)
	}
	if _, err := parseOnOffFlags(cmd, true, true); err == nil {
		t.Fatal("expected conflicting toggle error")
	}

	cmd = toggleTestCmd()
	if err := cmd.Flags().Set("on", "true"); err != nil {
		t.Fatal(err)
	}
	got, err := parseOnOffFlags(cmd, true, false)
	if err != nil || !got {
		t.Fatalf("--on = %v, %v; want true, nil", got, err)
	}

	cmd = toggleTestCmd()
	if err := cmd.Flags().Set("off", "true"); err != nil {
		t.Fatal(err)
	}
	got, err = parseOnOffFlags(cmd, false, true)
	if err != nil || got {
		t.Fatalf("--off = %v, %v; want false, nil", got, err)
	}
}

func TestParseOnOffFlagsRejectsExplicitFalseModes(t *testing.T) {
	cmd := toggleTestCmd()
	if err := cmd.Flags().Set("on", "false"); err != nil {
		t.Fatal(err)
	}
	if _, err := parseOnOffFlags(cmd, false, false); err == nil || !strings.Contains(err.Error(), "--on=false") {
		t.Fatalf("--on=false error = %v, want explicit false rejection", err)
	}

	cmd = toggleTestCmd()
	if err := cmd.Flags().Set("off", "false"); err != nil {
		t.Fatal(err)
	}
	if _, err := parseOnOffFlags(cmd, false, false); err == nil || !strings.Contains(err.Error(), "--off=false") {
		t.Fatalf("--off=false error = %v, want explicit false rejection", err)
	}
}

func toggleTestCmd() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.Flags().Bool("on", false, "")
	cmd.Flags().Bool("off", false, "")
	return cmd
}

func TestGroupsRequestsListRegistered(t *testing.T) {
	cmd := newGroupsCmd(&rootFlags{})
	got, _, err := cmd.Find([]string{"requests", "list"})
	if err != nil || got == nil || got.Name() != "list" {
		t.Fatalf("groups requests list not registered: got=%v err=%v", got, err)
	}
	if f := got.Flags().Lookup("jid"); f == nil {
		t.Fatal("--jid flag missing from groups requests list")
	}
}

func TestGroupsRequestsActionRegistered(t *testing.T) {
	cmd := newGroupsCmd(&rootFlags{})
	for _, sub := range []string{"approve", "reject"} {
		got, _, err := cmd.Find([]string{"requests", sub})
		if err != nil || got == nil || got.Name() != sub {
			t.Fatalf("groups requests %s not registered: got=%v err=%v", sub, got, err)
		}
		if f := got.Flags().Lookup("jid"); f == nil {
			t.Fatalf("--jid flag missing from groups requests %s", sub)
		}
		if f := got.Flags().Lookup("user"); f == nil {
			t.Fatalf("--user flag missing from groups requests %s", sub)
		}
	}
}

func TestResolveRequestEntriesResolved(t *testing.T) {
	lid, _ := types.ParseJID("12345@lid")
	pn, _ := types.ParseJID("48600000001@s.whatsapp.net")
	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	requests := []types.GroupParticipantRequest{{JID: lid, RequestedAt: ts}}
	resolver := func(_ context.Context, j types.JID) types.JID { return pn }

	entries := resolveRequestEntries(context.Background(), requests, resolver)

	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if entries[0].JID != lid.String() {
		t.Errorf("JID = %q, want %q", entries[0].JID, lid.String())
	}
	if entries[0].PhoneNumber != "+48600000001" {
		t.Errorf("PhoneNumber = %q, want %q", entries[0].PhoneNumber, "+48600000001")
	}
	if !entries[0].RequestedAt.Equal(ts) {
		t.Errorf("RequestedAt = %v, want %v", entries[0].RequestedAt, ts)
	}
}

func TestResolveRequestEntriesUnresolved(t *testing.T) {
	lid, _ := types.ParseJID("99999@lid")
	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	requests := []types.GroupParticipantRequest{{JID: lid, RequestedAt: ts}}
	// resolver returns the LID unchanged (not in store)
	resolver := func(_ context.Context, j types.JID) types.JID { return j }

	entries := resolveRequestEntries(context.Background(), requests, resolver)

	if entries[0].PhoneNumber != "" {
		t.Errorf("PhoneNumber = %q, want empty for unresolved LID", entries[0].PhoneNumber)
	}
	if entries[0].JID != lid.String() {
		t.Errorf("JID = %q, want %q", entries[0].JID, lid.String())
	}
}

func TestRequestListEntryJSONCompatibility(t *testing.T) {
	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	// resolved entry: all three keys present
	e := requestListEntry{JID: "12345@lid", PhoneNumber: "+48600000001", RequestedAt: ts}
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"JID", "RequestedAt", "phone_number"} {
		if _, ok := m[key]; !ok {
			t.Errorf("JSON output missing key %q", key)
		}
	}

	// unresolved entry: phone_number must be omitted (omitempty)
	e2 := requestListEntry{JID: "12345@lid", RequestedAt: ts}
	data2, _ := json.Marshal(e2)
	var m2 map[string]any
	_ = json.Unmarshal(data2, &m2)
	if _, ok := m2["phone_number"]; ok {
		t.Error("JSON should omit 'phone_number' when empty (omitempty)")
	}
}

func TestRequestListEntryTableColumnOrder(t *testing.T) {
	// Table output must preserve existing field order: JID, timestamp, phone_number
	// (phone appended as third field so existing consumers reading field[1] as timestamp are unaffected)
	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	lid, _ := types.ParseJID("12345@lid")
	pn, _ := types.ParseJID("48600000001@s.whatsapp.net")

	requests := []types.GroupParticipantRequest{{JID: lid, RequestedAt: ts}}
	resolver := func(_ context.Context, j types.JID) types.JID { return pn }
	entries := resolveRequestEntries(context.Background(), requests, resolver)

	e := entries[0]
	line := fmt.Sprintf("%s\t%s\t%s", e.JID, e.RequestedAt.Local().Format("2006-01-02 15:04:05"), e.PhoneNumber)
	fields := strings.Split(line, "\t")
	if len(fields) != 3 {
		t.Fatalf("expected 3 tab-separated fields, got %d: %q", len(fields), line)
	}
	if fields[0] != lid.String() {
		t.Errorf("field[0] (JID) = %q, want %q", fields[0], lid.String())
	}
	wantTS := ts.Local().Format("2006-01-02 15:04:05")
	if fields[1] != wantTS {
		t.Errorf("field[1] (timestamp) = %q, want %q", fields[1], wantTS)
	}
	if fields[2] != "+48600000001" {
		t.Errorf("field[2] (phone) = %q, want %q", fields[2], "+48600000001")
	}
}

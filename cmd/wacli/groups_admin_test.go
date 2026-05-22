package main

import (
	"strings"
	"testing"

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

package main

import "testing"

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
}

func TestContactsSaveRequiresName(t *testing.T) {
	cmd := newContactsSaveCmd(&rootFlags{})
	cmd.SetArgs([]string{"--jid", "5511999990000"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.Execute(); err == nil || err.Error() != "--name is required" {
		t.Fatalf("err = %v", err)
	}
}

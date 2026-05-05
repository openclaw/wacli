package main

import (
	"bytes"
	"testing"
)

func TestVersionCommandUsesConfiguredOutput(t *testing.T) {
	var out bytes.Buffer
	cmd := newVersionCmd()
	cmd.SetOut(&out)
	cmd.SetArgs(nil)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("version command: %v", err)
	}
	if got, want := out.String(), version+"\n"; got != want {
		t.Fatalf("version output = %q, want %q", got, want)
	}
}

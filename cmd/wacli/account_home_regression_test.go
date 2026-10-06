package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsolateAccountConfigHomeIsolatesWindowsHome(t *testing.T) {
	t.Setenv("USERPROFILE", filepath.Join(t.TempDir(), "outside"))
	isolateAccountConfigHome(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("USERPROFILE") != os.Getenv("HOME") || home != os.Getenv("HOME") {
		t.Fatal("account tests must isolate both Unix and Windows home resolution")
	}
}

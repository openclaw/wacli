package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openclaw/wacli/internal/store"
)

func TestJSONClosedStdoutHelper(t *testing.T) {
	path := os.Getenv("WACLI_CLOSED_STDOUT_TEST_STORE")
	if path == "" {
		t.Skip("subprocess helper")
	}
	os.Args = []string{"wacli", "--store", path, "--read-only", "--json", "groups", "list"}
	if os.Getenv("WACLI_CLOSED_STDOUT_TEST_ERROR") == "1" {
		os.Args = append(os.Args, "--invalid-flag")
	}
	main()
	os.Exit(0)
}

func TestJSONClosedStdout(t *testing.T) {
	path := t.TempDir()
	db, err := store.Open(filepath.Join(path, "wacli.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, commandError := range []bool{false, true} {
		name := "success"
		if commandError {
			name = "command error"
		}
		t.Run(name, func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			_ = r.Close()
			defer w.Close()
			cmd := exec.Command(os.Args[0], "-test.run=^TestJSONClosedStdoutHelper$")
			cmd.Env = append(os.Environ(), "WACLI_CLOSED_STDOUT_TEST_STORE="+path)
			if commandError {
				cmd.Env = append(cmd.Env, "WACLI_CLOSED_STDOUT_TEST_ERROR=1")
			}
			cmd.Stdout = w
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			err = cmd.Run()
			if commandError {
				if err == nil || cmd.ProcessState.ExitCode() != 1 || !strings.Contains(stderr.String(), "unknown flag") {
					t.Fatalf("command error lost: %v, stderr=%q", err, stderr.String())
				}
			} else if err != nil || stderr.Len() != 0 {
				t.Fatalf("closed stdout: %v, stderr=%q", err, stderr.String())
			}
		})
	}
}

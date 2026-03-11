package lock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestLockBlocksOtherProcess(t *testing.T) {
	if os.Getenv("WACLI_LOCK_HELPER") == "1" {
		dir := os.Getenv("WACLI_LOCK_DIR")
		lk, err := Acquire(dir)
		if err == nil {
			_ = lk.Release()
			_, _ = os.Stdout.WriteString("UNEXPECTED_OK\n")
			os.Exit(2)
		}
		if !strings.Contains(err.Error(), "store is locked") {
			_, _ = fmt.Fprintf(os.Stdout, "UNEXPECTED_ERR:%v\n", err)
			os.Exit(3)
		}
		if !errors.Is(err, ErrLocked) {
			_, _ = fmt.Fprintf(os.Stdout, "UNEXPECTED_NOT_ERRLOCKED:%v\n", err)
			os.Exit(4)
		}
		var ce *ContentionError
		if !errors.As(err, &ce) {
			_, _ = fmt.Fprintf(os.Stdout, "UNEXPECTED_NOT_CONTENTION:%v\n", err)
			os.Exit(5)
		}
		_, _ = os.Stdout.WriteString("EXPECTED_LOCKED\n")
		return
	}

	dir := t.TempDir()

	lk, err := Acquire(dir)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer lk.Release()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestLockBlocksOtherProcess")
	cmd.Env = append(os.Environ(),
		"WACLI_LOCK_HELPER=1",
		"WACLI_LOCK_DIR="+dir,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper failed: %v output=%s", err, strings.TrimSpace(string(out)))
	}
	got := string(out)
	if strings.Contains(got, "UNEXPECTED_OK") || strings.Contains(got, "UNEXPECTED_ERR:") {
		t.Fatalf("unexpected helper output: %q", strings.TrimSpace(got))
	}
	if !strings.Contains(got, "EXPECTED_LOCKED") {
		t.Fatalf("expected helper to report locked; output=%q", strings.TrimSpace(got))
	}
}

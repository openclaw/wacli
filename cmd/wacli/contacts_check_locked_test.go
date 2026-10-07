package main

import (
	"context"
	"strings"
	"testing"

	"github.com/openclaw/wacli/internal/lock"
)

func TestContactsCheckUsesLockedStoreDelegate(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	storeDir := shortPresenceDelegateStoreDir(t)
	lk, err := lock.Acquire(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	defer lk.Release()
	stop, err := startSendDelegateServerForStore(context.Background(), storeDir, sendSpacing{}, func(context.Context, sendDelegateRequest) (sendDelegateResponse, error) {
		return sendDelegateResponse{OK: true}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	stdout, stderr, err := runPresenceDelegateHelper(t, []string{
		"--store", storeDir, "--json", "--timeout", "750ms", "contacts", "check", "+15550000001",
	})
	if err != nil {
		t.Fatalf("contacts check: %v stdout=%q stderr=%q", err, stdout, stderr)
	}
}

func TestContactsCheckReadOnlyRejectsBeforeDelegation(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	for _, mode := range []string{"flag", "environment"} {
		t.Run(mode, func(t *testing.T) {
			storeDir := shortPresenceDelegateStoreDir(t)
			lk, err := lock.Acquire(storeDir)
			if err != nil {
				t.Fatal(err)
			}
			defer lk.Release()
			called := make(chan struct{}, 1)
			stop, err := startSendDelegateServerForStore(context.Background(), storeDir, sendSpacing{}, func(context.Context, sendDelegateRequest) (sendDelegateResponse, error) {
				called <- struct{}{}
				return sendDelegateResponse{OK: true}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			args := []string{"--store", storeDir, "--json"}
			t.Setenv("WACLI_READONLY", "")
			if mode == "flag" {
				args = append(args, "--read-only")
			} else {
				t.Setenv("WACLI_READONLY", "1")
			}
			args = append(args, "contacts", "check", "+15550000001")
			stdout, stderr, err := runPresenceDelegateHelper(t, args)
			if err == nil || !strings.Contains(stdout+stderr, "read-only") {
				t.Fatalf("want read-only rejection, got %v stdout=%q stderr=%q", err, stdout, stderr)
			}
			select {
			case <-called:
				t.Fatal("read-only check reached the delegate")
			default:
			}
		})
	}
}

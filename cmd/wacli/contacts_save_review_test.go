package main

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/openclaw/wacli/internal/lock"
)

// Ported from the independent contacts-save branch: run the real CLI entry
// point against the production socket server, without opening a WA session.
func TestReview500ContactsSaveDelegatesWhenLocked(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	dir := shortPresenceDelegateStoreDir(t)
	lk, err := lock.Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lk.Release()
	called := make(chan sendDelegateRequest, 1)
	stop, err := startSendDelegateServerForStore(context.Background(), dir, sendSpacing{}, func(_ context.Context, req sendDelegateRequest) (sendDelegateResponse, error) {
		called <- req
		return sendDelegateResponse{OK: true}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	stdout, stderr, err := runPresenceDelegateHelper(t, []string{
		"--store", dir, "--json", "--timeout", "750ms", "contacts", "save",
		"--jid", "+1 555 000 1111", "--name", "Ada Lovelace", "--first-name", "Ada", "--phonebook",
	})
	select {
	case req := <-called:
		if req.Kind != "contacts_save" || req.To != "15550001111@s.whatsapp.net" || req.Name != "Ada Lovelace" {
			t.Fatalf("delegate request = %+v", req)
		}
	default:
		t.Fatalf("contacts save never reached the available delegate: err=%v stdout=%q stderr=%q", err, stdout, stderr)
	}
	if strings.Contains(stdout+stderr, "store is locked") {
		t.Fatalf("save returned a store lock error despite an available delegate: %s%s", stdout, stderr)
	}
}

func TestReview500ContactsSaveReadOnlyBeforeDelegation(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	for _, mode := range []string{"flag", "environment"} {
		t.Run(mode, func(t *testing.T) {
			dir := shortPresenceDelegateStoreDir(t)
			lk, err := lock.Acquire(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer lk.Release()
			called := make(chan struct{}, 1)
			stop, err := startSendDelegateServerForStore(context.Background(), dir, sendSpacing{}, func(context.Context, sendDelegateRequest) (sendDelegateResponse, error) {
				called <- struct{}{}
				return sendDelegateResponse{OK: true}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			t.Setenv("WACLI_READONLY", "")
			args := []string{"--store", dir, "--json"}
			if mode == "flag" {
				args = append(args, "--read-only")
			} else {
				t.Setenv("WACLI_READONLY", "1")
			}
			args = append(args, "contacts", "save", "--jid", "15550001111", "--name", "Ada")
			stdout, stderr, err := runPresenceDelegateHelper(t, args)
			if err == nil || !strings.Contains(stdout+stderr, "read-only") {
				t.Fatalf("want read-only rejection: err=%v stdout=%q stderr=%q", err, stdout, stderr)
			}
			select {
			case <-called:
				t.Fatal("read-only save reached the delegate")
			default:
			}
		})
	}
}

func TestReview500ContactsSavePositionalArguments(t *testing.T) {
	cmd := newContactsCmd(&rootFlags{storeDir: t.TempDir(), readOnly: true})
	cmd.SetArgs([]string{"save", "--jid", "15550001111", "--name", "Ada", "extra"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err == nil || strings.Contains(err.Error(), "read-only") {
		t.Fatalf("want positional rejection before RunE, got %v", err)
	}
}

func TestReview500ContactsSaveMalformedPhoneJID(t *testing.T) {
	for _, raw := range []string{"@s.whatsapp.net", "abc@s.whatsapp.net"} {
		t.Run(raw, func(t *testing.T) {
			if jid, err := parseContactSaveTarget(raw); err == nil {
				t.Fatalf("malformed phone JID accepted: %q -> %s", raw, jid)
			}
		})
	}
}

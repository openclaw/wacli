//go:build darwin

package syscontacts

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestReadContactsCommand(t *testing.T) {
	for _, mode := range []string{"boundary", "oversized", "descendant", "inherited", "denied", "stderr", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestContactsHelperProcess$")
			cmd.Env = append(os.Environ(), "WACLI_TEST_CONTACTS_HELPER="+mode)
			contacts, err := readContactsCommand(cmd)
			if ctx.Err() != nil {
				t.Fatal("helper was not stopped before its deadline")
			}
			switch mode {
			case "boundary":
				if err != nil || len(contacts) != 1 || contacts[0].Name() != "Alice" {
					t.Fatalf("10 MiB helper: contacts=%v err=%v", contacts, err)
				}
			case "oversized", "descendant":
				if err == nil || !strings.Contains(err.Error(), "contacts export too large") {
					t.Fatalf("oversized helper error = %v", err)
				}
			case "denied":
				if err == nil || !strings.Contains(err.Error(), "Contacts access denied") {
					t.Fatalf("helper diagnostic lost: %v", err)
				}
			case "inherited":
				if !errors.Is(err, exec.ErrWaitDelay) {
					t.Fatalf("inherited pipe error = %v, want WaitDelay", err)
				}
			case "stderr":
				if err == nil || len(err.Error()) > (64<<10)+100 {
					t.Fatal("helper stderr was not bounded")
				}
			case "invalid":
				if err == nil {
					t.Fatal("invalid helper JSON accepted")
				}
			}
		})
	}
}

func TestContactsHelperProcess(t *testing.T) {
	mode := os.Getenv("WACLI_TEST_CONTACTS_HELPER")
	if mode == "" {
		return
	}
	switch mode {
	case "inherited":
		child := exec.Command(os.Args[0], "-test.run=^TestContactsHelperProcess$")
		child.Env = append(os.Environ(), "WACLI_TEST_CONTACTS_HELPER=hold")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(1)
		}
	case "hold":
		_, _ = fmt.Fprint(os.Stdout, "[]")
		time.Sleep(time.Minute)
	case "descendant":
		child := exec.Command(os.Args[0], "-test.run=^TestContactsHelperProcess$")
		child.Env = append(os.Environ(), "WACLI_TEST_CONTACTS_HELPER=oversized")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Run(); err != nil {
			os.Exit(1)
		}
	case "boundary":
		const contact = `[{"full_name":"Alice","phones":["+15551234567"]}]`
		_, _ = fmt.Fprint(os.Stdout, contact)
		_, _ = io.CopyN(os.Stdout, repeatReader{ch: ' '}, int64(MaxContactsDecodeBytes-len(contact)))
	case "oversized":
		_, _ = io.CopyN(os.Stdout, repeatReader{ch: ' '}, int64(MaxContactsDecodeBytes)+1)
		time.Sleep(time.Minute)
	case "denied":
		_, _ = fmt.Fprint(os.Stdout, "[")
		_, _ = fmt.Fprint(os.Stderr, "Contacts access denied")
		os.Exit(1)
	case "stderr":
		_, _ = io.CopyN(os.Stderr, repeatReader{ch: 'x'}, 1<<20)
		os.Exit(1)
	case "invalid":
		_, _ = fmt.Fprint(os.Stdout, "[")
	}
	os.Exit(0)
}

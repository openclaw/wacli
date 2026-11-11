package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/openclaw/wacli/internal/lock"
)

func TestExactReadIDsValidation(t *testing.T) {
	flags := &rootFlags{}
	if newChatsMarkReadCmd(flags, true).Flags().Lookup("message-id") == nil {
		t.Fatal("mark-read has no --message-id flag")
	}
	if newChatsMarkReadCmd(flags, false).Flags().Lookup("message-id") != nil {
		t.Fatal("mark-unread must not accept --message-id")
	}
	many := make([]string, 101)
	for i := range many {
		many[i] = fmt.Sprintf("id-%d", i)
	}
	for _, tc := range []struct {
		ids      []string
		receipts bool
		through  string
		want     string
	}{
		{[]string{"a"}, false, "", "requires --receipts"},
		{[]string{"a"}, true, "b", "mutually exclusive"},
		{nil, true, "", "takes 1 to 100"},
		{many, true, "", "takes 1 to 100"},
		{[]string{" a"}, true, "", "exact nonempty"},
		{[]string{""}, true, "", "exact nonempty"},
		{[]string{"a", "a"}, true, "", "repeated"},
	} {
		if err := validateExactReadIDs(tc.ids, tc.receipts, tc.through); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%v receipts=%t through=%q: err = %v, want %q", tc.ids, tc.receipts, tc.through, err, tc.want)
		}
	}
	if err := validateExactReadIDs([]string{"a", "b"}, true, ""); err != nil {
		t.Fatal(err)
	}
	var err error
	captureRootStderr(t, func() {
		err = execute([]string{"--store", t.TempDir(), "chats", "mark-read", "--chat", "123@s.whatsapp.net", "--message-id", "a"})
	})
	if err == nil || !strings.Contains(err.Error(), "requires --receipts") {
		t.Fatalf("--message-id without --receipts: %v", err)
	}

	fake := &orderedMarkReadApp{}
	unread := false
	for _, req := range []sendDelegateRequest{
		{Kind: markReadIDsKind, Receipts: true},
		{Kind: markReadIDsKind, MessageIDs: []string{"a"}},
		{Kind: markReadIDsKind, MessageIDs: []string{"a"}, Receipts: true, Read: &unread},
		{Kind: markReadIDsKind, MessageIDs: []string{"a"}, Receipts: true, ThroughMessageID: "a"},
		{Kind: markReadIDsKind, MessageIDs: []string{"a", "a"}, Receipts: true},
		{Kind: markReadReceiptsKind, MessageIDs: []string{"a"}, Receipts: true},
		{Kind: markReadThroughKind, MessageIDs: []string{"a"}, ThroughMessageID: "a", Receipts: true},
	} {
		if _, err := executeDelegatedMarkRead(context.Background(), fake, req); err == nil {
			t.Fatalf("invalid exact request accepted: %+v", req)
		}
	}
	if len(fake.steps) != 0 {
		t.Fatalf("invalid request reached updates: %v", fake.steps)
	}
	resp, err := executeDelegatedMarkRead(context.Background(), fake, sendDelegateRequest{Kind: markReadIDsKind, To: "123@s.whatsapp.net", MessageIDs: []string{"a", "b"}, Receipts: true})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fake.steps, []string{"exact 123@s.whatsapp.net a,b"}) || resp.Receipts == nil || *resp.Receipts != 2 || resp.UnreadSettled == nil || !*resp.UnreadSettled {
		t.Fatalf("exact request = steps %v resp %+v", fake.steps, resp)
	}
}

// The real CLI hands the exact IDs to a running sync process as their own
// kind, and an owner without exact support refuses it without any fallback.
func TestExactReadIDsDelegateAndOldOwnerRejects(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	for _, supported := range []bool{true, false} {
		t.Run(map[bool]string{true: "current owner", false: "old owner"}[supported], func(t *testing.T) {
			storeDir := shortPresenceDelegateStoreDir(t)
			lk, err := lock.Acquire(storeDir)
			if err != nil {
				t.Fatal(err)
			}
			defer lk.Release()
			requests := make(chan sendDelegateRequest, 4)
			fake := &orderedMarkReadApp{}
			stop, err := startSendDelegateServerForStore(context.Background(), storeDir, sendSpacing{}, func(ctx context.Context, req sendDelegateRequest) (sendDelegateResponse, error) {
				requests <- req
				if !supported && req.Kind != markReadKind && req.Kind != markReadReceiptsKind && req.Kind != markReadThroughKind {
					return sendDelegateResponse{}, fmt.Errorf("unsupported send kind %q", req.Kind)
				}
				return executeDelegatedMarkRead(ctx, fake, req)
			})
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			args := []string{"--store", storeDir, "--json", "--timeout", "750ms", "chats", "mark-read", "--chat", "123@s.whatsapp.net", "--receipts", "--message-id", "a", "--message-id", "b"}
			stdout, stderr, err := runPresenceDelegateHelper(t, args)
			req := <-requests
			if req.Kind != markReadIDsKind || !slices.Equal(req.MessageIDs, []string{"a", "b"}) || !req.Receipts || req.ThroughMessageID != "" {
				t.Fatalf("wrong IPC request: %+v", req)
			}
			select {
			case req := <-requests:
				t.Fatalf("unexpected retry or fallback: %+v", req)
			default:
			}
			if supported {
				if err != nil {
					t.Fatalf("delegated exact mark-read failed: %v stderr=%q", err, stderr)
				}
				for _, want := range []string{`"ok":true`, `"receipts":2`, `"unread_settled":true`, `"receipt":"unknown"`} {
					if !strings.Contains(stdout, want) {
						t.Fatalf("stdout %q missing %s", stdout, want)
					}
				}
				return
			}
			if err == nil || stdout != "" || !strings.Contains(stderr, "does not support --message-id") || !strings.Contains(stderr, "left the chat unread") {
				t.Fatalf("old owner rejection: err=%v stdout=%q stderr=%q", err, stdout, stderr)
			}
			if len(fake.steps) != 0 {
				t.Fatalf("old owner reached updates: %v", fake.steps)
			}
		})
	}
}

package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/store"

	"github.com/openclaw/wacli/internal/lock"
	"github.com/openclaw/wacli/internal/wa"
)

func TestMessagesDeleteDelegatesAdminRevokeWhenStoreLocked(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	storeDir := shortPresenceDelegateStoreDir(t)
	lk, err := lock.Acquire(storeDir)
	if err != nil {
		t.Fatalf("lock store: %v", err)
	}
	defer lk.Release()

	server := startPresenceDelegateTestSocket(t, storeDir, func(req sendDelegateRequest) sendDelegateResponse {
		return sendDelegateResponse{OK: true, Sent: true, To: "123@g.us", ID: "revoke-id", Target: req.ID}
	})
	defer server.stop()

	stdout, stderr, err := runPresenceDelegateHelper(t, []string{
		"--store", storeDir, "--json", "--timeout", "750ms",
		"messages", "delete", "--chat", "123@g.us", "--id", "ABC", "--sender", "999@lid", "--post-send-wait", "25ms",
	})
	if err != nil {
		t.Fatalf("messages delete failed: %v stdout=%q stderr=%q", err, stdout, stderr)
	}
	req := server.nextRequest(t)
	if req.Kind != deleteDelegateKind || req.To != "123@g.us" || req.ID != "ABC" || req.Sender != "999@lid" || req.PostSendWaitMS != 25 {
		t.Fatalf("delegate request = %+v", req)
	}
	for _, want := range []string{`"revoked":true`, `"id":"revoke-id"`, `"target":"ABC"`, `"delegated":true`} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout %q missing %s", stdout, want)
		}
	}
}

func TestGroupsParticipantsRemoveDelegatesWhenStoreLocked(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	storeDir := shortPresenceDelegateStoreDir(t)
	lk, err := lock.Acquire(storeDir)
	if err != nil {
		t.Fatalf("lock store: %v", err)
	}
	defer lk.Release()

	server := startPresenceDelegateTestSocket(t, storeDir, func(req sendDelegateRequest) sendDelegateResponse {
		return sendDelegateResponse{OK: true, To: req.To, Action: req.ParticipantAction,
			Participants: []byte(`[{"JID":"4912345@s.whatsapp.net","Error":0}]`)}
	})
	defer server.stop()

	stdout, stderr, err := runPresenceDelegateHelper(t, []string{
		"--store", storeDir, "--json", "--timeout", "750ms",
		"groups", "participants", "remove", "--jid", "123@g.us", "--user", "+4912345",
	})
	if err != nil {
		t.Fatalf("participants remove failed: %v stdout=%q stderr=%q", err, stdout, stderr)
	}
	req := server.nextRequest(t)
	if req.Kind != participantsDelegateKind || req.To != "123@g.us" || req.ParticipantAction != "remove" ||
		len(req.Users) != 1 || req.Users[0] != "+4912345" {
		t.Fatalf("delegate request = %+v", req)
	}
	if !strings.Contains(stdout, `"success":true`) || !strings.Contains(stdout, "4912345@s.whatsapp.net") {
		t.Fatalf("stdout %q", stdout)
	}
}

func TestMessagesDeleteForMeDoesNotDelegate(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	storeDir := shortPresenceDelegateStoreDir(t)
	lk, err := lock.Acquire(storeDir)
	if err != nil {
		t.Fatalf("lock store: %v", err)
	}
	defer lk.Release()
	server := startPresenceDelegateTestSocket(t, storeDir, func(req sendDelegateRequest) sendDelegateResponse {
		return sendDelegateResponse{OK: true}
	})
	defer server.stop()
	_, stderr, err := runPresenceDelegateHelper(t, []string{
		"--store", storeDir, "--json", "--timeout", "750ms",
		"messages", "delete", "--chat", "123@g.us", "--id", "ABC", "--for-me",
	})
	if err == nil || !strings.Contains(stderr, "store is locked") {
		t.Fatalf("for-me delete should fail on locked store, err=%v stderr=%q", err, stderr)
	}
}

func TestValidateParticipantJID(t *testing.T) {
	for in, ok := range map[string]bool{"+4912345": true, "4912345@s.whatsapp.net": true, "123456789@lid": true, "@@@": false, "x@g.us": false} {
		j, err := wa.ParseUserOrJID(in)
		if err == nil {
			err = validateParticipantJID(j)
		}
		if (err == nil) != ok {
			t.Fatalf("%q: err=%v, want ok=%v", in, err, ok)
		}
	}
}

func TestMessagesDeleteDelegatedWarningsReachCallerStderr(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	storeDir := shortPresenceDelegateStoreDir(t)
	lk, err := lock.Acquire(storeDir)
	if err != nil {
		t.Fatalf("lock store: %v", err)
	}
	defer lk.Release()

	server := startPresenceDelegateTestSocket(t, storeDir, func(req sendDelegateRequest) sendDelegateResponse {
		return sendDelegateResponse{OK: true, Sent: true, To: "123@g.us", ID: "revoke-id", Target: req.ID,
			SendWarning: "warning: rapid \x1b[31msend", StoreWarning: "disk full"}
	})
	defer server.stop()

	stdout, stderr, err := runPresenceDelegateHelper(t, []string{
		"--store", storeDir, "--json", "--timeout", "750ms",
		"messages", "delete", "--chat", "123@g.us", "--id", "ABC",
	})
	if err != nil {
		t.Fatalf("messages delete failed: %v stdout=%q stderr=%q", err, stdout, stderr)
	}
	_ = server.nextRequest(t)
	if !strings.Contains(stderr, "warning: rapid") || !strings.Contains(stderr, "disk full") || strings.Contains(stderr, "\x1b") {
		t.Fatalf("stderr = %q, want sanitized delegated warnings", stderr)
	}
	if !strings.Contains(stdout, `"store_warning":"disk full"`) || strings.Contains(stdout, "rapid") {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestGroupsParticipantsRejectsInvalidInputBeforeDelegating(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"non-group jid", []string{"--jid", "4912345@s.whatsapp.net", "--user", "+4912345"}, "invalid group"},
		{"group as user", []string{"--jid", "123@g.us", "--user", "456@g.us"}, "invalid user"},
		{"garbage user", []string{"--jid", "123@g.us", "--user", "abc"}, "invalid user"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storeDir := shortPresenceDelegateStoreDir(t)
			lk, err := lock.Acquire(storeDir)
			if err != nil {
				t.Fatalf("lock store: %v", err)
			}
			defer lk.Release()
			calls := 0
			server := startPresenceDelegateTestSocket(t, storeDir, func(req sendDelegateRequest) sendDelegateResponse {
				calls++
				return sendDelegateResponse{OK: true}
			})
			defer server.stop()
			args := append([]string{"--store", storeDir, "--json", "--timeout", "750ms", "groups", "participants", "add"}, tc.args...)
			_, stderr, err := runPresenceDelegateHelper(t, args)
			if err == nil || !strings.Contains(stderr, tc.want) {
				t.Fatalf("err=%v stderr=%q, want %q", err, stderr, tc.want)
			}
			if calls != 0 {
				t.Fatalf("invalid request reached the sync process")
			}
		})
	}
}

func TestExecuteDelegatedGroupParticipantsValidatesBeforeAppUse(t *testing.T) {
	for _, req := range []sendDelegateRequest{
		{Kind: participantsDelegateKind, To: "123@g.us", ParticipantAction: "kick", Users: []string{"+4912345"}},
		{Kind: participantsDelegateKind, To: "123@g.us", ParticipantAction: "add"},
		{Kind: participantsDelegateKind, To: "4912345@s.whatsapp.net", ParticipantAction: "add", Users: []string{"+4912345"}},
		{Kind: participantsDelegateKind, To: "123@g.us", ParticipantAction: "remove", Users: []string{"status@broadcast"}},
	} {
		// A nil app panics if reached, proving validation happens first.
		if _, err := executeDelegatedGroupParticipants(context.Background(), nil, req); err == nil {
			t.Fatalf("request %+v accepted", req)
		}
	}
}

func TestExecuteDelegatedDeleteValidatesSenderBeforeSending(t *testing.T) {
	a, err := app.New(app.Options{StoreDir: t.TempDir()})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	t.Cleanup(a.Close)
	group := "120363000000000000@g.us"
	now := time.Now().UTC()
	if err := a.DB().UpsertChat(group, "group", "G", now); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := a.DB().UpsertMessage(store.UpsertMessageParams{
		ChatJID: group, MsgID: "OTHER", SenderJID: "4917@s.whatsapp.net", Timestamp: now, Text: "hi",
	}); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}
	for sender, want := range map[string]string{"123-456@g.us": "not a group", "abc": "invalid sender"} {
		_, err := executeDelegatedDelete(context.Background(), a, sendDelegateRequest{Kind: deleteDelegateKind, To: group, ID: "OTHER", Sender: sender})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("sender %q: err=%v, want %q", sender, err, want)
		}
	}
}

package main

import (
	"strings"
	"testing"
	"time"

	"github.com/steipete/wacli/internal/store"
	"go.mau.fi/whatsmeow/types"
)

func openSendTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(t.TempDir() + "/wacli.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestResolveReplySenderFromStore(t *testing.T) {
	db := openSendTestDB(t)
	chat := types.JID{User: "12345", Server: types.GroupServer}
	sender := "15551234567@s.whatsapp.net"

	if err := db.UpsertChat(chat.String(), "group", "Group", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := db.UpsertMessage(store.UpsertMessageParams{
		ChatJID:   chat.String(),
		MsgID:     "quoted",
		SenderJID: sender,
		Timestamp: time.Now(),
		Text:      "hello",
	}); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}

	got, err := resolveReplySender(db, chat, "quoted", "")
	if err != nil {
		t.Fatalf("resolveReplySender: %v", err)
	}
	if got.String() != sender {
		t.Fatalf("sender = %q, want %q", got.String(), sender)
	}
}

func TestResolveReplySenderOverride(t *testing.T) {
	db := openSendTestDB(t)
	chat := types.JID{User: "12345", Server: types.GroupServer}

	got, err := resolveReplySender(db, chat, "missing", "+15551234567")
	if err != nil {
		t.Fatalf("resolveReplySender: %v", err)
	}
	if got.String() != "15551234567@s.whatsapp.net" {
		t.Fatalf("sender = %q", got.String())
	}
}

func TestResolveReplySenderRequiresGroupSenderWhenMissing(t *testing.T) {
	db := openSendTestDB(t)
	chat := types.JID{User: "12345", Server: types.GroupServer}

	_, err := resolveReplySender(db, chat, "missing", "")
	if err == nil || !strings.Contains(err.Error(), "--reply-to-sender is required") {
		t.Fatalf("expected group sender error, got %v", err)
	}
}

func TestResolveReplySenderAllowsDirectMessageWithoutSender(t *testing.T) {
	db := openSendTestDB(t)
	chat := types.JID{User: "15551234567", Server: types.DefaultUserServer}

	got, err := resolveReplySender(db, chat, "missing", "")
	if err != nil {
		t.Fatalf("resolveReplySender: %v", err)
	}
	if !got.IsEmpty() {
		t.Fatalf("expected empty sender for direct reply, got %q", got.String())
	}
}

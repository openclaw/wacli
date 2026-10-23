package store

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Optional old-binary compatibility check. The only store it can open is this
// test's disposable directory, and the command is explicitly read-only.
func TestSemanticKindReadableByPreviousBinary(t *testing.T) {
	bin := os.Getenv("WACLI_COMPAT_BIN")
	if bin == "" {
		t.Skip("set WACLI_COMPAT_BIN to the previous binary")
	}
	dir := t.TempDir()
	db := mustOpen(t, filepath.Join(dir, "wacli.db"))
	chat := "123@s.whatsapp.net"
	seedOutgoing(t, db, chat, "m1", time.Now())
	if err := db.UpsertMessage(UpsertMessageParams{ChatJID: chat, MsgID: "m1", Timestamp: time.Now(), Kind: "contact", Text: "fixture contact"}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	output, err := exec.Command(bin, "--store", dir, "--read-only", "--json", "messages", "list", "--chat", chat, "--limit", "10").CombinedOutput()
	if err != nil || !bytes.Contains(output, []byte("fixture contact")) {
		t.Fatalf("old reader: %s (%v)", output, err)
	}
}

func TestSemanticKindMigrationAndLIDMerge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wacli.db")
	db := mustOpen(t, path)
	chat := "123@lid"
	seedOutgoing(t, db, chat, "m1", time.Now())
	if _, err := db.sql.Exec(`DROP TRIGGER retrieval_messages_update; ALTER TABLE messages DROP COLUMN kind; DELETE FROM schema_migrations WHERE version = 35`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.sql.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := createMessageChangeTriggers(tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	db.Close()
	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	old, err := ro.GetMessage(chat, "m1")
	if err != nil || old.Kind != "" {
		t.Fatalf("%+v: %v", old, err)
	}
	ro.Close()
	db = mustOpen(t, path)
	defer db.Close()
	if !db.kindEnabled {
		t.Fatal("migration did not add kind")
	}
	var trigger string
	if err := db.sql.QueryRow(`SELECT sql FROM sqlite_master WHERE name='retrieval_messages_update'`).Scan(&trigger); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(trigger, "old.kind IS NOT new.kind") {
		t.Fatal("kind does not invalidate retrieval cursors")
	}
	if err := db.UpsertMessage(UpsertMessageParams{ChatJID: chat, MsgID: "m1", Timestamp: time.Now(), FromMe: true, Kind: "poll", Text: "Poll: test"}); err != nil {
		t.Fatal(err)
	}
	// An old writer omits kind; it must not erase the new classification.
	if err := db.UpsertMessage(UpsertMessageParams{ChatJID: chat, MsgID: "m1", Timestamp: time.Now(), FromMe: true, Text: "Poll: test"}); err != nil {
		t.Fatal(err)
	}
	if err := db.MigrateLIDToPN(chat, "456@s.whatsapp.net"); err != nil {
		t.Fatal(err)
	}
	msg, err := db.GetMessage("456@s.whatsapp.net", "m1")
	if err != nil || msg.Kind != "poll" || msg.MediaType != "" {
		t.Fatalf("%+v: %v", msg, err)
	}
	if err := db.MarkMessageRevoked("456@s.whatsapp.net", "m1"); err != nil {
		t.Fatal(err)
	}
	msg, err = db.GetMessage("456@s.whatsapp.net", "m1")
	if err != nil || msg.Kind != "revoked" {
		t.Fatalf("%+v: %v", msg, err)
	}
}

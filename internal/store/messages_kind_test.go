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
	if _, err := db.sql.Exec(`DROP TRIGGER retrieval_messages_update; ALTER TABLE messages DROP COLUMN kind; DELETE FROM schema_migrations WHERE version = 37`); err != nil {
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

func storedKind(t *testing.T, db *DB, chat, msgID string) any {
	t.Helper()
	var kind any
	if err := db.sql.QueryRow(`SELECT kind FROM messages WHERE chat_jid = ? AND msg_id = ?`, chat, msgID).Scan(&kind); err != nil {
		t.Fatal(err)
	}
	return kind
}

func seedKinds(t *testing.T, db *DB, chat string, base time.Time) {
	t.Helper()
	if err := db.UpsertChat(chat, "dm", "Alice", base); err != nil {
		t.Fatal(err)
	}
	for i, m := range []UpsertMessageParams{
		{MsgID: "poll", Kind: "poll", DisplayText: "Poll: Pizza?"},
		{MsgID: "text", Text: "which one?"},
		{MsgID: "card", Kind: "contact", DisplayText: "Contact: Alice"},
	} {
		m.ChatJID, m.Timestamp = chat, base.Add(time.Duration(i)*time.Minute)
		if err := db.UpsertMessage(m); err != nil {
			t.Fatal(err)
		}
	}
}

func kindsByID(msgs []Message) map[string]string {
	out := map[string]string{}
	for _, m := range msgs {
		out[m.MsgID] = m.Kind
	}
	return out
}

// messages list, search and export page through ReadView, which must carry the
// column capability like it carries receipts and FTS.
func TestSemanticKindThroughReadView(t *testing.T) {
	db := mustOpen(t, filepath.Join(t.TempDir(), "wacli.db"))
	chat := "123@s.whatsapp.net"
	seedKinds(t, db, chat, time.Now().Add(-time.Hour))
	var listed, found []Message
	if err := db.ReadView(func(view *DB) error {
		var err error
		if listed, err = view.ListMessages(ListMessagesParams{ChatJID: chat, Limit: 10}); err != nil {
			return err
		}
		found, err = view.SearchMessages(SearchMessagesParams{Query: "Pizza", ChatJID: chat, Limit: 10})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got := kindsByID(listed); got["poll"] != "poll" || got["card"] != "contact" || got["text"] != "" {
		t.Fatalf("list kinds = %v", got)
	}
	if got := kindsByID(found); got["poll"] != "poll" {
		t.Fatalf("search kinds = %v", got)
	}
	page, err := db.ListMessagesPage("messages list", ListMessagesParams{ChatJID: chat, Limit: 10}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := kindsByID(page.Messages); got["poll"] != "poll" || got["card"] != "contact" {
		t.Fatalf("page kinds = %v", got)
	}
}

func TestSemanticKindOfContextNeighbors(t *testing.T) {
	db := mustOpen(t, filepath.Join(t.TempDir(), "wacli.db"))
	chat := "123@s.whatsapp.net"
	seedKinds(t, db, chat, time.Now().Add(-time.Hour))
	msgs, err := db.MessageContext(chat, "text", 5, 5)
	if err != nil {
		t.Fatal(err)
	}
	if got := kindsByID(msgs); len(msgs) != 3 || got["poll"] != "poll" || got["card"] != "contact" || got["text"] != "" {
		t.Fatalf("context kinds = %v", got)
	}
}

// Purge is the payload-erasure path (#309): the kind says what the message
// was, so it goes with the payload, and neither a replay of the original event
// nor the phone/LID merge brings it back.
func TestPurgeClearsSemanticKind(t *testing.T) {
	db := mustOpen(t, filepath.Join(t.TempDir(), "wacli.db"))
	chat := "123@s.whatsapp.net"
	base := time.Now().Add(-time.Hour)
	seedKinds(t, db, chat, base)
	if err := db.MarkMessageDeletedForMe(chat, "card", "", false, base.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.PurgeMessage(chat, "card"); err != nil {
		t.Fatal(err)
	}
	if kind := storedKind(t, db, chat, "card"); kind != nil {
		t.Fatalf("stored kind after purge = %v", kind)
	}
	if err := db.UpsertMessage(UpsertMessageParams{ChatJID: chat, MsgID: "card", Timestamp: base, Kind: "contact", DisplayText: "Contact: Alice"}); err != nil {
		t.Fatal(err)
	}
	m, err := db.GetMessage(chat, "card")
	if err != nil || m.Kind != "" {
		t.Fatalf("after replay %+v: %v", m.Kind, err)
	}

	pn, lid := "15551234567@s.whatsapp.net", "999123456789@lid"
	for _, c := range []string{pn, lid} {
		if err := db.UpsertChat(c, "dm", "Bob", base); err != nil {
			t.Fatal(err)
		}
		if err := db.UpsertMessage(UpsertMessageParams{ChatJID: c, MsgID: "vcard", Timestamp: base, Kind: "contact", DisplayText: "Contact: Bob"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.MarkMessageDeletedForMe(lid, "vcard", "", false, base.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.PurgeMessage(lid, "vcard"); err != nil {
		t.Fatal(err)
	}
	if err := db.MigrateLIDToPN(lid, pn); err != nil {
		t.Fatal(err)
	}
	if kind := storedKind(t, db, pn, "vcard"); kind != nil {
		t.Fatalf("stored kind after LID merge of a purged message = %v", kind)
	}
}

// A store without the change journal (retrieval tables only) keeps working:
// the kind migration must not install triggers that write to a missing table.
func TestSemanticKindMigrationOnStoreWithoutChangeJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wacli.db")
	db := mustOpen(t, path)
	chat := "123@s.whatsapp.net"
	seedOutgoing(t, db, chat, "m1", time.Now())
	rows, err := db.sql.Query(`SELECT name FROM sqlite_master WHERE type='trigger' AND sql LIKE '%message_changes%'`)
	if err != nil {
		t.Fatal(err)
	}
	var triggers []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		triggers = append(triggers, name)
	}
	rows.Close()
	for _, name := range triggers {
		if _, err := db.sql.Exec(`DROP TRIGGER "` + name + `"`); err != nil {
			t.Fatal(err)
		}
	}
	// What a retrieval-only store keeps: an update trigger that predates the
	// journal and does not write to it.
	if _, err := db.sql.Exec(`DROP TABLE message_changes; ALTER TABLE messages DROP COLUMN kind;
		CREATE TRIGGER retrieval_messages_update AFTER UPDATE ON messages BEGIN SELECT 1; END`); err != nil {
		t.Fatal(err)
	}
	if err := migrateMessageKind(db); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMessage(UpsertMessageParams{ChatJID: chat, MsgID: "m2", Timestamp: time.Now(), Kind: "poll", DisplayText: "Poll: x?"}); err != nil {
		t.Fatalf("write after migration on a journal-free store: %v", err)
	}
	if kind := storedKind(t, db, chat, "m2"); kind != "poll" {
		t.Fatalf("kind = %v", kind)
	}
}

// Another migration may hold version 37 in a store (open proposals renumber);
// the writable open still adds the column through ensureCurrentSchema.
func TestSemanticKindColumnRepairedWhenVersionRowIsTaken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wacli.db")
	db := mustOpen(t, path)
	if _, err := db.sql.Exec(`DROP TRIGGER retrieval_messages_update; ALTER TABLE messages DROP COLUMN kind; UPDATE schema_migrations SET name = 'some other proposal' WHERE version = 37`); err != nil {
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
	db = mustOpen(t, path)
	if !db.kindEnabled {
		t.Fatal("kind column not repaired on open")
	}
}

// The opposite merge direction: the phone-number copy was purged and the LID
// copy still carries the kind.
func TestPurgedPhoneCopyKeepsKindClearedThroughLIDMerge(t *testing.T) {
	db := mustOpen(t, filepath.Join(t.TempDir(), "wacli.db"))
	base := time.Now().Add(-time.Hour)
	pn, lid := "15551234567@s.whatsapp.net", "999123456789@lid"
	for _, c := range []string{pn, lid} {
		if err := db.UpsertChat(c, "dm", "Bob", base); err != nil {
			t.Fatal(err)
		}
		if err := db.UpsertMessage(UpsertMessageParams{ChatJID: c, MsgID: "vcard", Timestamp: base, Kind: "contact", DisplayText: "Contact: Bob"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.MarkMessageDeletedForMe(pn, "vcard", "", false, base.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.PurgeMessage(pn, "vcard"); err != nil {
		t.Fatal(err)
	}
	if err := db.MigrateLIDToPN(lid, pn); err != nil {
		t.Fatal(err)
	}
	if kind := storedKind(t, db, pn, "vcard"); kind != nil {
		t.Fatalf("stored kind = %v", kind)
	}
}

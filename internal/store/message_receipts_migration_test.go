package store

import (
	"path/filepath"
	"testing"
	"time"
)

// A store written before receipts were kept has no table and no way to get one
// from the core schema, which only ever runs once.
func TestOpeningAnOlderStoreAddsTheReceiptTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wacli.db")
	db := mustOpen(t, path)
	if _, err := db.sql.Exec(`DROP TABLE message_receipts`); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	if _, err := db.sql.Exec(`DELETE FROM schema_migrations WHERE name = 'message receipts'`); err != nil {
		t.Fatalf("forget the migration: %v", err)
	}
	if db.hasTable("message_receipts") {
		t.Fatal("the table survived the drop")
	}
	db.Close()

	reopened := mustOpen(t, path)
	defer reopened.Close()
	if !reopened.hasTable("message_receipts") {
		t.Fatal("reopening an older store did not add the receipt table")
	}
	if !reopened.receiptsEnabled {
		t.Fatal("the reopened store does not report receipts as available")
	}

	chat := "123@s.whatsapp.net"
	at := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	seedOutgoing(t, reopened, chat, "m1", at)
	if err := reopened.UpsertMessageReceipt(chat, "m1", chat, "read", at); err != nil {
		t.Fatalf("UpsertMessageReceipt: %v", err)
	}
	counts, err := reopened.MessageReceipts(chat, "m1")
	if err != nil || counts.Read != 1 {
		t.Fatalf("counts = %+v, err = %v; want one read", counts, err)
	}
}

// A store opened read-only cannot be migrated, so the counts have to answer
// zero instead of failing on a table that is not there.
func TestReadOnlyOlderStoreReportsNoReceipts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wacli.db")
	db := mustOpen(t, path)
	chat := "123@s.whatsapp.net"
	at := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	seedOutgoing(t, db, chat, "m1", at)
	if _, err := db.sql.Exec(`DROP TABLE message_receipts`); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	db.Close()

	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer ro.Close()
	msgs, err := ro.ListMessages(ListMessagesParams{ChatJID: chat, Limit: 5})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 1 || msgs[0].DeliveredTo != 0 || msgs[0].ReadBy != 0 {
		t.Fatalf("messages = %+v, want one message with no receipt counts", msgs)
	}
}

func mustOpen(t *testing.T, path string) *DB {
	t.Helper()
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return db
}

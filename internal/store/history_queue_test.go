package store

import (
	"testing"
	"time"
)

func TestHistorySyncQueueKeepsTheAccountOfEachRow(t *testing.T) {
	db := openTestDB(t)
	queuedAt := time.Date(2026, 9, 27, 11, 45, 0, 0, time.UTC)
	if err := db.EnqueueHistorySync("hist-1", "15550000001@s.whatsapp.net", 3, []byte{0x08, 0x03}, queuedAt); err != nil {
		t.Fatalf("EnqueueHistorySync: %v", err)
	}
	if err := db.EnqueueHistorySync("hist-2", " ", 3, []byte{0x08, 0x03}, queuedAt); err == nil {
		t.Fatalf("EnqueueHistorySync without an account succeeded")
	}
	item, ok, err := db.NextHistorySync(nil)
	if err != nil || !ok {
		t.Fatalf("NextHistorySync = %v, %v", ok, err)
	}
	if item.MsgID != "hist-1" || item.AccountJID != "15550000001@s.whatsapp.net" || !item.QueuedAt.Equal(queuedAt) {
		t.Fatalf("queued item = %+v", item)
	}
	if _, ok, err := db.NextHistorySync([]int64{item.ID}); err != nil || ok {
		t.Fatalf("NextHistorySync skipping the only row = %v, %v, want none", ok, err)
	}
}

func TestHistorySyncQueueGainsTheAccountColumn(t *testing.T) {
	db := openTestDB(t)
	// The table as the first version of the queue made it, with a row in it.
	if _, err := db.sql.Exec(`DROP TABLE history_sync_queue`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if _, err := db.sql.Exec(`
		CREATE TABLE history_sync_queue (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			msg_id TEXT UNIQUE,
			sync_type INTEGER NOT NULL,
			notification BLOB NOT NULL,
			queued_at INTEGER NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0
		)`); err != nil {
		t.Fatalf("create old table: %v", err)
	}
	if _, err := db.sql.Exec(`INSERT INTO history_sync_queue(msg_id, sync_type, notification, queued_at) VALUES('hist-old', 3, x'0803', 1)`); err != nil {
		t.Fatalf("insert old row: %v", err)
	}

	if err := db.ensureCurrentSchema(); err != nil {
		t.Fatalf("ensureCurrentSchema: %v", err)
	}
	has, err := db.tableHasColumn("history_sync_queue", "account_jid")
	if err != nil || !has {
		t.Fatalf("account_jid column = %v (err %v), want added", has, err)
	}
	item, ok, err := db.NextHistorySync(nil)
	if err != nil || !ok || item.MsgID != "hist-old" || item.AccountJID != "" {
		t.Fatalf("old row = %+v, %v, %v: want it kept with no account", item, ok, err)
	}
}

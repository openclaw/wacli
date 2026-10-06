package store

import (
	"path/filepath"
	"testing"
)

func benchmarkReadStore(b *testing.B) *DB {
	b.Helper()
	db, err := Open(filepath.Join(b.TempDir(), "archive.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = db.Close() })
	_, err = db.sql.Exec(`INSERT INTO chats(jid, kind) VALUES ('15550000000@s.whatsapp.net', 'dm'), ('10000000000@lid', 'dm')`)
	if err != nil {
		b.Fatal(err)
	}
	_, err = db.sql.Exec(`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<50000)
 INSERT INTO messages(chat_jid,msg_id,ts,from_me,text,display_text)
 SELECT CASE WHEN x%2=0 THEN '15550000000@s.whatsapp.net' ELSE '10000000000@lid' END, printf('m%d',x), x, 0, 'synthetic history', 'synthetic history' FROM n`)
	if err != nil {
		b.Fatal(err)
	}
	return db
}

func BenchmarkListMessagesAliasChat(b *testing.B) {
	db := benchmarkReadStore(b)
	p := ListMessagesParams{ChatJIDs: []string{"15550000000@s.whatsapp.net", "10000000000@lid"}, Limit: 50}
	for b.Loop() {
		rows, err := db.ListMessages(p)
		if err != nil || len(rows) != 50 || rows[0].MsgID != "m50000" {
			b.Fatalf("rows=%d error=%v", len(rows), err)
		}
	}
}

func BenchmarkDetectMessagesFTS(b *testing.B) {
	db := benchmarkReadStore(b)
	if !db.HasFTS() {
		b.Skip("requires sqlite_fts5")
	}
	for b.Loop() {
		if !db.detectMessagesFTS() {
			b.Fatal("FTS disappeared")
		}
	}
}

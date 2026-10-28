package store

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestQuickRepliesOrderingResolutionAndCoverage(t *testing.T) {
	db := openTestDB(t)
	at := time.Unix(100, 0)
	count := int32(2)
	q := QuickReply{ID: "1", Shortcut: "hello", Message: "A", Count: &count, Keywords: []string{"k"}, LabelIDs: []string{"7"}}
	if err := db.UpsertQuickReply(q, at); err != nil {
		t.Fatal(err)
	}
	q.Message = "B"
	if err := db.UpsertQuickReply(q, at.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"1", "hello", "/hello"} {
		got, err := db.ResolveQuickReply(ref)
		if err != nil || !reflect.DeepEqual(got, q) {
			t.Fatalf("resolve=%+v %v", got, err)
		}
	}
	got, err := db.QuickReplies()
	if err != nil || got.Completeness != "incomplete" {
		t.Fatalf("coverage=%+v %v", got, err)
	}
	if _, err := db.ResolveQuickReply("missing"); !errors.Is(err, ErrQuickReplyNotFound) {
		t.Fatalf("not found=%v", err)
	}
	q.Deleted = true
	if err := db.UpsertQuickReply(q, at.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, err := db.QuickReplies(); err != nil || len(got.QuickReplies) != 0 {
		t.Fatalf("deleted visible=%+v %v", got, err)
	}
	if q, found, err := db.QuickReplyByID("1"); err != nil || !found || !q.Deleted {
		t.Fatalf("tombstone=%+v %v %v", q, found, err)
	}
	for _, id := range []string{"2", "3"} {
		if err := db.UpsertQuickReply(QuickReply{ID: id, Shortcut: "dup"}, at); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ResolveQuickReply("dup"); err == nil || errors.Is(err, ErrQuickReplyNotFound) {
		t.Fatalf("ambiguous=%v", err)
	}
	if err := db.MarkRegularFullSyncEventsSeen(); err != nil {
		t.Fatal(err)
	}
	if got, err := db.QuickReplies(); err != nil || got.Completeness != "complete" {
		t.Fatalf("full coverage=%+v %v", got, err)
	}
}

func TestQuickReplyMigrationCollisionAndReadOnlyUpgrade(t *testing.T) {
	for _, version := range []int{34, 36, 37, 38} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "wacli.db")
			db := mustOpen(t, path)
			if _, err := db.sql.Exec(`DROP TABLE quick_replies; DROP TABLE quick_reply_sync_state;
			DELETE FROM schema_migrations WHERE version IN (38,?);
			INSERT INTO schema_migrations(version,name,applied_at) VALUES(?,'foreign owner',1);
			CREATE TABLE list_sync_state(id INTEGER PRIMARY KEY, full_sync_events_seen INTEGER);
			INSERT INTO list_sync_state VALUES(1,1);`, version, version); err != nil {
				t.Fatal(err)
			}
			db.Close()
			ro, err := OpenReadOnly(path)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := ro.QuickReplies(); err != nil || got.Completeness != "incomplete" || len(got.QuickReplies) != 0 {
				t.Fatalf("legacy read-only=%+v %v", got, err)
			}
			if ro.hasTable("quick_replies") {
				t.Fatal("read-only migrated")
			}
			ro.Close()
			db = mustOpen(t, path)
			defer db.Close()
			if got, err := db.QuickReplies(); err != nil || got.Completeness != "incomplete" {
				t.Fatalf("inherited foreign coverage=%+v %v", got, err)
			}
			if err := db.UpsertQuickReply(QuickReply{ID: "1", Shortcut: "test"}, time.Unix(1, 0)); err != nil {
				t.Fatal(err)
			}
			var owner string
			if err := db.sql.QueryRow(`SELECT name FROM schema_migrations WHERE version=?`, version).Scan(&owner); err != nil || owner != "foreign owner" {
				t.Fatalf("foreign row overwritten=%q %v", owner, err)
			}
			if err := db.MarkRegularFullSyncEventsSeen(); err != nil {
				t.Fatal(err)
			}
			db.Close()
			db = mustOpen(t, path)
			if got, err := db.QuickReplies(); err != nil || got.Completeness != "complete" || len(got.QuickReplies) != 1 {
				t.Fatalf("reopen=%+v %v", got, err)
			}
			db.Close()
		})
	}
}

func TestQuickReplyReadOnlyWithoutCoverageTableIsIncomplete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wacli.db")
	db := mustOpen(t, path)
	if err := db.MarkRegularFullSyncEventsSeen(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`DROP TABLE quick_reply_sync_state`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if got, err := ro.QuickReplies(); err != nil || got.Completeness != "incomplete" {
		t.Fatalf("missing coverage=%+v %v", got, err)
	}
}

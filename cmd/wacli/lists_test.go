package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
)

// This also runs unchanged on the base: the new command, rather than a new Go
// API, is the regression boundary. All fixtures are disposable and offline.
func TestListsCLIReadOnlyJSON(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "wacli.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertChat("123@s.whatsapp.net", "dm", "Fixture", time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	db.Close()
	for _, name := range []string{"lists", "labels"} {
		raw := captureRootStdout(t, func() {
			if err := execute([]string{"--store", dir, "--read-only", "--json", name, "list"}); err != nil {
				t.Fatal(err)
			}
		})
		var result struct {
			Success bool `json:"success"`
			Data    struct {
				Lists              []any  `json:"lists"`
				Completeness       string `json:"completeness"`
				FullSyncEventsSeen bool   `json:"full_sync_events_seen"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			t.Fatal(err)
		}
		if !result.Success || result.Data.Lists == nil || len(result.Data.Lists) != 0 || result.Data.Completeness != "unknown" || result.Data.FullSyncEventsSeen {
			t.Fatalf("empty index claimed completeness or wrong JSON: %s", raw)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "session.db")); !os.IsNotExist(err) {
		t.Fatalf("read command created a WhatsApp session: %v", err)
	}
}

func TestListsCLIJSONAndChatFilter(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "wacli.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, jid := range []string{"123@s.whatsapp.net", "456@s.whatsapp.net"} {
		if err := db.UpsertChat(jid, "dm", "Fixture", time.Unix(100, 0)); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	// SQL fixtures keep this test independent of the new event/store APIs.
	seed, err := sql.Open("sqlite3", filepath.Join(dir, "wacli.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = seed.Exec(`INSERT INTO lists(id,name,color,predefined_id,type,order_index,is_active,is_immutable,updated_at)
		VALUES('7','Sales',3,0,5,2,1,0,100),('8','Groups',NULL,NULL,2,NULL,NULL,NULL,100),
			('2','Promo',NULL,NULL,5,NULL,NULL,NULL,100),('9','2',NULL,NULL,5,NULL,NULL,NULL,100);
		INSERT INTO chat_lists(chat_jid,list_id,labeled,updated_at) VALUES('123@s.whatsapp.net','7',1,100),
			('123@s.whatsapp.net','2',1,100),('456@s.whatsapp.net','9',1,100);
		INSERT INTO lists(id,name,deleted,updated_at) VALUES('4','Deleted',1,100);
		UPDATE list_sync_state SET full_sync_events_seen=1 WHERE id=1;`)
	seed.Close()
	if err != nil {
		t.Fatal(err)
	}
	raw := captureRootStdout(t, func() {
		if err := execute([]string{"--store", dir, "--read-only", "--json", "lists", "list"}); err != nil {
			t.Fatal(err)
		}
	})
	for _, field := range []string{`"name":"Sales"`, `"color":3`, `"type":5`, `"predefined_id":0`, `"order_index":2`, `"is_active":true`, `"is_immutable":false`, `"full_sync_events_seen":true`, `"completeness":"unknown"`} {
		if !strings.Contains(raw, field) {
			t.Fatalf("missing %s: %s", field, raw)
		}
	}
	for _, value := range []string{"7", "Sales", "2", "Promo"} {
		raw = captureRootStdout(t, func() {
			if err := execute([]string{"--store", dir, "--read-only", "--json", "chats", "list", "--list", value}); err != nil {
				t.Fatal(err)
			}
		})
		var result struct {
			Data []store.Chat `json:"data"`
		}
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Data) != 1 || result.Data[0].JID != "123@s.whatsapp.net" {
			t.Fatalf("filter=%s: %s", value, raw)
		}
	}
	captureRootStderr(t, func() {
		if err := execute([]string{"--store", dir, "--read-only", "chats", "list", "--list", "8"}); err == nil || !strings.Contains(err.Error(), "unsupported membership type") {
			t.Fatalf("system filter = %v", err)
		}
	})
	for _, tc := range []struct{ value, want string }{
		{"4", `list "4" was deleted on the phone`},
		{"Deleted", `list "Deleted" was deleted on the phone`},
		{"404", `list "404" is not in the local index; sync first`},
		{"missing", `list "missing" is not in the local index; sync first`},
	} {
		captureRootStderr(t, func() {
			if err := execute([]string{"--store", dir, "--read-only", "--json", "chats", "list", "--list", tc.value}); err == nil || err.Error() != tc.want {
				t.Errorf("filter=%s error = %v, want %s", tc.value, err, tc.want)
			}
		})
	}
}

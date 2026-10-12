package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// The retrieval harness drives real CLI subprocesses against disposable stores.
// WACLI_RETRIEVAL_ARTIFACTS selects an outside-repository transcript directory.
type retrievalCLI struct {
	t   *testing.T
	bin string
	log bytes.Buffer
}

func newRetrievalCLI(t *testing.T, a *App) *retrievalCLI {
	t.Helper()
	h := &retrievalCLI{t: t, bin: filepath.Join(t.TempDir(), "wacli")}
	args := []string{"build", "-o", h.bin}
	if a.db.HasFTS() {
		args = append(args, "-tags", "sqlite_fts5")
	}
	args = append(args, "./cmd/wacli")
	c := exec.Command("go", args...)
	c.Dir = "../.."
	b, e := c.CombinedOutput()
	if e != nil {
		t.Fatalf("build: %v %s", e, b)
	}
	mode := "plain"
	if a.db.HasFTS() {
		mode = "fts"
	}
	t.Cleanup(func() {
		dir := os.Getenv("WACLI_RETRIEVAL_ARTIFACTS")
		if dir == "" {
			dir = t.TempDir()
		}
		_ = os.MkdirAll(dir, 0700)
		rev := exec.Command("git", "rev-parse", "HEAD")
		rev.Dir = "../.."
		r, _ := rev.Output()
		diff := exec.Command("git", "diff", "--binary")
		diff.Dir = "../.."
		d, _ := diff.Output()
		untracked := exec.Command("git", "ls-files", "--others", "--exclude-standard")
		untracked.Dir = "../.."
		files, _ := untracked.Output()
		for _, file := range strings.Split(strings.TrimSpace(string(files)), "\n") {
			if file == "" {
				continue
			}
			add := exec.Command("git", "diff", "--no-index", "--binary", "/dev/null", file)
			add.Dir = "../.."
			b, _ := add.Output()
			d = append(d, b...)
		}
		fmt.Fprintf(&h.log, "repeat: WACLI_RETRIEVAL_ARTIFACTS=%q go test %s./internal/app -run '^%s$' -count=1\n", dir, map[bool]string{true: "-tags sqlite_fts5 ", false: ""}[a.db.HasFTS()], t.Name())
		fmt.Fprintf(&h.log, "revision: %s\nfailed: %v\n", r, t.Failed())
		_ = os.WriteFile(filepath.Join(dir, t.Name()+"-"+mode+".log"), h.log.Bytes(), 0600)
		_ = os.WriteFile(filepath.Join(dir, t.Name()+"-"+mode+".diff"), d, 0600)
	})
	return h
}
func (h *retrievalCLI) run(dir string, fail string, args ...string) map[string]any {
	return h.invoke(dir, fail, true, args...)
}
func (h *retrievalCLI) write(dir string, args ...string) map[string]any {
	return h.invoke(dir, "", false, args...)
}
func (h *retrievalCLI) invoke(dir string, fail string, readOnly bool, args ...string) map[string]any {
	h.t.Helper()
	argv := []string{"--store", dir, "--json"}
	if readOnly {
		argv = append(argv, "--read-only")
	}
	argv = append(argv, args...)
	c := exec.Command(h.bin, argv...)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	b, e := c.Output()
	fmt.Fprintf(&h.log, "command: %q\nstdout: %s\nstderr: %s\nexit: %v\n", argv, b, stderr.String(), e)
	if len(b) == 0 && e != nil {
		b = stderr.Bytes()
	}
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		h.t.Fatalf("JSON: %v; %s; %s", err, b, stderr.String())
	}
	if fail != "" {
		if e == nil || v["success"] != false || !strings.Contains(fmt.Sprint(v["error"]), fail) {
			h.t.Fatalf("want error %q: %s %v", fail, b, e)
		}
		return v
	}
	if e != nil || v["success"] != true {
		h.t.Fatalf("CLI: %s %v", b, e)
	}
	data, ok := v["data"].(map[string]any)
	if !ok {
		h.t.Fatalf("data: %s", b)
	}
	return data
}
func retrievalIDs(d map[string]any) []string {
	var ids []string
	rows, _ := d["messages"].([]any)
	for _, r := range rows {
		ids = append(ids, r.(map[string]any)["MsgID"].(string))
	}
	return ids
}
func retrievalSeed(t *testing.T, a *App, n int) {
	t.Helper()
	t.Logf("fixture: messages=%d; chat=123@s.whatsapp.net; base=1700000000; ties=9; IDs=mNNN; text=needle repetitions", n)
	for i := 0; i < n; i++ {
		if err := a.db.UpsertChat("123@s.whatsapp.net", "dm", "Fixture", time.Unix(1700000000, 0)); err != nil {
			t.Fatal(err)
		}
		if err := a.db.UpsertMessage(store.UpsertMessageParams{ChatJID: "123@s.whatsapp.net", MsgID: fmt.Sprintf("m%03d", i), Timestamp: time.Unix(1700000000+int64(i/9), 0), SenderJID: "123@s.whatsapp.net", Text: strings.Repeat("needle ", i%3+1)}); err != nil {
			t.Fatal(err)
		}
	}
}
func retrievalFingerprint(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	entries, e := os.ReadDir(dir)
	if e != nil {
		t.Fatal(e)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		p := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		info, _ := os.Stat(p)
		out[e.Name()] = fmt.Sprintf("%x/%d/%s/%o", sha256.Sum256(b), info.Size(), info.ModTime(), info.Mode())
	}
	return out
}
func retrievalSQL(t *testing.T, dir, query string, args ...any) {
	t.Helper()
	d, e := sql.Open("sqlite3", filepath.Join(dir, "wacli.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer d.Close()
	if _, e = d.Exec(query, args...); e != nil {
		t.Fatal(e)
	}
}
func TestRetrievalStage1CLI(t *testing.T) {
	a := newTestApp(t)
	retrievalSeed(t, a, 37)
	h := newRetrievalCLI(t, a)
	for _, args := range [][]string{{"messages", "list"}, {"messages", "list", "--asc"}, {"messages", "export"}, {"messages", "search", "needle"}} {
		full := retrievalIDs(h.run(a.StoreDir(), "", append(args, "--limit", "100")...))
		var got []string
		cursor := ""
		for pages := 0; pages < 20; pages++ {
			argv := append(append([]string{}, args...), "--limit", "5")
			if cursor != "" {
				argv = append(argv, "--cursor", cursor)
			}
			d := h.run(a.StoreDir(), "", argv...)
			got = append(got, retrievalIDs(d)...)
			p, ok := d["page"].(map[string]any)
			if !ok {
				t.Fatal("missing page metadata")
			}
			if !p["has_more"].(bool) {
				if p["next_cursor"] != nil {
					t.Fatal("final cursor")
				}
				break
			}
			cursor = p["next_cursor"].(string)
		}
		if !reflect.DeepEqual(got, full) {
			t.Fatalf("traversal %v: %v != %v", args, got, full)
		}
	}
	for _, n := range []int{0, 5, 6} {
		b := newTestApp(t)
		retrievalSeed(t, b, n)
		d := h.run(b.StoreDir(), "", "messages", "list", "--limit", "5")
		p := d["page"].(map[string]any)
		if p["has_more"] != (n > 5) {
			t.Fatalf("has_more n=%d: %v", n, p)
		}
	}

	// Page size can change without affecting traversal; filters/commands cannot.
	query := h.run(a.StoreDir(), "", "messages", "search", "needle", "--limit", "5")
	searchCursor := query["page"].(map[string]any)["next_cursor"].(string)
	h.run(a.StoreDir(), "filters", "messages", "search", "different", "--cursor", searchCursor)
	h.run(a.StoreDir(), "filters", "messages", "search", " needle ", "--cursor", searchCursor)
	h.run(a.StoreDir(), "filters", "messages", "export", "--cursor", searchCursor)
	h.run(a.StoreDir(), "", "messages", "search", "needle", "--cursor", searchCursor, "--limit", "3")
	if a.db.HasFTS() {
		other := &retrievalCLI{t: t, bin: filepath.Join(t.TempDir(), "wacli-plain")}
		build := exec.Command("go", "build", "-o", other.bin, "./cmd/wacli")
		build.Dir = "../.."
		if b, e := build.CombinedOutput(); e != nil {
			t.Fatalf("alternate build: %s %v", b, e)
		}
		other.run(a.StoreDir(), "backend", "messages", "search", "needle", "--cursor", searchCursor)
		h.log.Write(other.log.Bytes())
	}
	raw, e := json.Marshal(map[string]any{"v": 99})
	if e != nil {
		t.Fatal(e)
	}
	h.run(a.StoreDir(), "version", "messages", "list", "--cursor", base64.RawURLEncoding.EncodeToString(raw))
	h.run(a.StoreDir(), "", "messages", "list", "--limit", "100001")
	defaults := newTestApp(t)
	retrievalSeed(t, defaults, 55)
	if len(retrievalIDs(h.run(defaults.StoreDir(), "", "messages", "list"))) != 50 || len(retrievalIDs(h.run(defaults.StoreDir(), "", "messages", "search", "needle"))) != 50 || len(retrievalIDs(h.run(defaults.StoreDir(), "", "messages", "export"))) != 55 {
		t.Fatal("default limits changed")
	}
	for _, mutate := range []func() error{
		func() error {
			return a.db.SetStarred(store.SetStarredParams{ChatJID: "123@s.whatsapp.net", MsgID: "m000", Starred: true, StarredAt: time.Unix(1, 0)})
		},
		func() error { return a.db.UpsertChat("123@s.whatsapp.net", "dm", "Renamed", time.Unix(1700000010, 0)) },
	} {
		old := h.run(a.StoreDir(), "", "messages", "search", "needle", "--limit", "5")["page"].(map[string]any)["next_cursor"].(string)
		if e := mutate(); e != nil {
			t.Fatal(e)
		}
		h.run(a.StoreDir(), "changed", "messages", "search", "needle", "--cursor", old)
	}
	d := h.run(a.StoreDir(), "", "messages", "list", "--limit", "5")
	cur := d["page"].(map[string]any)["next_cursor"].(string)
	h.run(a.StoreDir(), "filters", "messages", "list", "--cursor", cur, "--asc")
	h.run(a.StoreDir(), "cursor", "messages", "list", "--cursor", "nonsense")
	b := newTestApp(t)
	h.run(b.StoreDir(), "store", "messages", "list", "--cursor", cur)
	if e := a.db.UpdateMessageText("123@s.whatsapp.net", "m000", "changed"); e != nil {
		t.Fatal(e)
	}
	h.run(a.StoreDir(), "changed", "messages", "list", "--cursor", cur)
	// A failed canonical message write cannot establish successful freshness.
	failed := newTestApp(t)
	retrievalSQL(t, failed.StoreDir(), `CREATE TRIGGER fixture_refuse BEFORE INSERT ON messages BEGIN SELECT RAISE(ABORT,'fixture storage fault');END`)
	retrievalSyncEvents(t, failed, retrievalLive("refused", "not stored", 100))
	if source := h.run(failed.StoreDir(), "", "messages", "list")["source"].(map[string]any); source["last_sync_at"] != nil {
		t.Fatal("failed ingestion recorded successful sync")
	}
	unknown := h.run(a.StoreDir(), "", "messages", "list")["source"].(map[string]any)
	if unknown["last_sync_at"] != nil {
		t.Fatal("unknown freshness must be null")
	}
	a.wa = newFakeWA()
	if _, e := a.Sync(context.Background(), SyncOptions{Mode: SyncModeOnce, IdleExit: time.Millisecond}); e != nil {
		t.Fatal(e)
	}
	fresh := h.run(a.StoreDir(), "", "messages", "list")["source"].(map[string]any)["last_sync_at"]
	ts, e := time.Parse(time.RFC3339, fmt.Sprint(fresh))
	if e != nil || time.Since(ts) > time.Minute {
		t.Fatalf("sync freshness: %v", fresh)
	}
	doc := h.run(a.StoreDir(), "", "doctor")
	if doc["store"].(map[string]any)["last_sync_at"] != fresh {
		t.Fatal("doctor freshness differs")
	}
	activeBefore := retrievalFingerprint(t, a.StoreDir())
	h.run(a.StoreDir(), "", "messages", "list")
	if !reflect.DeepEqual(activeBefore, retrievalFingerprint(t, a.StoreDir())) {
		t.Fatal("active read changed files")
	}
	a.Close()
	before := retrievalFingerprint(t, a.StoreDir())
	h.run(a.StoreDir(), "", "messages", "list")
	h.run(a.StoreDir(), "", "doctor")
	if !reflect.DeepEqual(before, retrievalFingerprint(t, a.StoreDir())) {
		t.Fatal("read-only changed files")
	}
	// A previous schema keeps first-page reads, with no persistent identity creation.
	legacy := newTestApp(t)
	retrievalSeed(t, legacy, 6)
	legacy.Close()
	retrievalSQL(t, legacy.StoreDir(), `DROP TABLE IF EXISTS retrieval_state`)
	before = retrievalFingerprint(t, legacy.StoreDir())
	old := h.run(legacy.StoreDir(), "", "messages", "list", "--limit", "5")
	if p, ok := old["page"].(map[string]any); ok && p["next_cursor"] != nil {
		t.Fatal("legacy cursor emitted")
	}
	h.run(legacy.StoreDir(), "migration", "messages", "list", "--cursor", cur)
	if !reflect.DeepEqual(before, retrievalFingerprint(t, legacy.StoreDir())) {
		t.Fatal("legacy read mutated store")
	}
}

func retrievalChanges(d map[string]any) []map[string]any {
	var out []map[string]any
	rows, _ := d["changes"].([]any)
	for _, r := range rows {
		out = append(out, r.(map[string]any))
	}
	return out
}
func retrievalSyncEvents(t *testing.T, a *App, eventsIn ...any) {
	t.Helper()
	for _, evt := range eventsIn {
		b, _ := json.Marshal(evt)
		t.Logf("fake WhatsApp event: %s", b)
	}
	f := newFakeWA()
	f.connectEvents = eventsIn
	a.wa = f
	if _, err := a.Sync(context.Background(), SyncOptions{Mode: SyncModeOnce, IdleExit: time.Millisecond}); err != nil {
		t.Fatal(err)
	}
}
func retrievalLive(id, text string, ts int64) *events.Message {
	jid := types.NewJID("123", types.DefaultUserServer)
	return &events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: jid, Sender: jid}, ID: id, Timestamp: time.Unix(ts, 0)}, Message: &waE2E.Message{Conversation: proto.String(text)}}
}

// Upgrade real archives at the writable CLI boundary. The SQL fixture removes
// only objects absent from the released/prerelease schema being represented.
func retrievalUpgradeFixtures(t *testing.T, parent *retrievalCLI) {
	for _, fixture := range []string{"prerelease-journal", "upstream-0.21", "prerelease-retrieval-only", "prerelease-interrupted-marker"} {
		t.Run(fixture, func(t *testing.T) {
			h := &retrievalCLI{t: t, bin: parent.bin}
			t.Cleanup(func() { parent.log.Write(h.log.Bytes()) })
			a := newTestApp(t)
			retrievalSeed(t, a, 4)
			if err := a.db.MarkMessageDeletedForMe("123@s.whatsapp.net", "m002", "", false, time.Unix(1700000010, 0)); err != nil {
				t.Fatal(err)
			}
			retrievalSQL(t, a.StoreDir(), `UPDATE messages SET from_me=1 WHERE msg_id='m000'`)
			if err := a.db.RecordSuccessfulSync(time.Unix(1700000100, 0)); err != nil {
				t.Fatal(err)
			}
			a.Close()
			query := `DELETE FROM schema_migrations WHERE version>=29;`
			journal := fixture == "prerelease-journal" || fixture == "prerelease-interrupted-marker"
			if !journal {
				query += `DROP TRIGGER retrieval_messages_insert;DROP TRIGGER retrieval_messages_update;DROP TRIGGER retrieval_messages_delete;
DROP TRIGGER changes_payload_purge;DROP TABLE message_changes;DROP TABLE change_state;`
			}
			if fixture == "upstream-0.21" {
				for _, table := range []string{"chats", "contacts", "contact_aliases", "starred", "message_locations"} {
					for _, op := range []string{"insert", "update", "delete"} {
						query += "DROP TRIGGER IF EXISTS retrieval_" + table + "_" + op + ";"
					}
				}
				query += `DROP TABLE retrieval_state;
INSERT INTO schema_migrations VALUES(29,'message receipts',13);`
			} else {
				query += `DROP TABLE message_receipts;
INSERT INTO schema_migrations VALUES(29,'retrieval identity, revision and sync state',13);`
				if journal {
					query += `INSERT INTO schema_migrations VALUES(30,'content-free message change journal',14);`
				} else {
					for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
						query += `CREATE TRIGGER retrieval_messages_` + strings.ToLower(op) + ` AFTER ` + op + ` ON messages BEGIN UPDATE retrieval_state SET revision=revision+1 WHERE id=1; END;`
					}
				}
				if fixture == "prerelease-interrupted-marker" {
					// An earlier writable open recorded the new journal number before
					// reconciling the conflicting prerelease markers.
					query += `INSERT INTO schema_migrations VALUES(31,'content-free message change journal',15);`
				}
			}
			fmt.Fprintf(&h.log, "upgrade fixture %s: %s\n", fixture, query)
			retrievalSQL(t, a.StoreDir(), query)
			before := retrievalFingerprint(t, a.StoreDir())
			messages := h.run(a.StoreDir(), "", "messages", "list", "--limit", "100")
			tombstone := h.run(a.StoreDir(), "", "messages", "show", "--chat", "123@s.whatsapp.net", "--id", "m002")
			var cursor string
			var continuation map[string]any
			if fixture != "upstream-0.21" {
				page := h.run(a.StoreDir(), "", "messages", "list", "--limit", "1")
				cursor = page["page"].(map[string]any)["next_cursor"].(string)
				continuation = h.run(a.StoreDir(), "", "messages", "list", "--limit", "1", "--cursor", cursor)
			}
			var checkpoint, bounded string
			var nextChanges map[string]any
			if journal {
				checkpoint = h.run(a.StoreDir(), "", "messages", "changes", "--checkpoint")["next_checkpoint"].(string)
				bounded = h.run(a.StoreDir(), "", "messages", "changes", "--since", "beginning", "--limit", "1")["next_checkpoint"].(string)
				nextChanges = h.run(a.StoreDir(), "", "messages", "changes", "--since", bounded, "--limit", "1")
			} else {
				h.run(a.StoreDir(), "migration", "messages", "changes", "--checkpoint")
			}
			if !reflect.DeepEqual(before, retrievalFingerprint(t, a.StoreDir())) {
				t.Fatal("pre-upgrade read-only CLI changed the archive")
			}
			// A normal writable CLI open owns migration and its repeat behavior.
			for i := 0; i < 2; i++ {
				h.write(a.StoreDir(), "doctor")
			}
			if got := h.run(a.StoreDir(), "", "messages", "list", "--limit", "100"); !reflect.DeepEqual(messages, got) {
				t.Fatalf("upgrade changed messages or freshness: before=%v after=%v", messages, got)
			}
			if got := h.run(a.StoreDir(), "", "messages", "show", "--chat", "123@s.whatsapp.net", "--id", "m002"); !reflect.DeepEqual(tombstone, got) {
				t.Fatal("upgrade changed deletion metadata")
			}
			if cursor != "" && !reflect.DeepEqual(continuation, h.run(a.StoreDir(), "", "messages", "list", "--limit", "1", "--cursor", cursor)) {
				t.Fatal("upgrade invalidated existing page cursor")
			}
			if journal {
				if got := h.run(a.StoreDir(), "", "messages", "changes", "--checkpoint")["next_checkpoint"]; got != checkpoint {
					t.Fatal("upgrade changed journal identity or high water")
				}
				if !reflect.DeepEqual(nextChanges, h.run(a.StoreDir(), "", "messages", "changes", "--since", bounded, "--limit", "1")) {
					t.Fatal("upgrade changed bounded journal replay")
				}
			} else {
				seed := retrievalChanges(h.run(a.StoreDir(), "", "messages", "changes", "--since", "beginning"))
				if len(seed) != 4 {
					t.Fatalf("upgrade seeded %d rows, want four retained messages", len(seed))
				}
				checkpoint = h.run(a.StoreDir(), "", "messages", "changes", "--checkpoint")["next_checkpoint"].(string)
			}
			writer, err := store.Open(filepath.Join(a.StoreDir(), "wacli.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Close()
			if err = writer.UpsertMessageReceipt("123@s.whatsapp.net", "m000", "123@s.whatsapp.net", "read", time.Unix(1700000200, 0)); err != nil {
				t.Fatalf("upgrade did not enable receipt writes: %v", err)
			}
			for _, args := range [][]string{{"messages", "list", "--limit", "10"}, {"messages", "search", "needle", "--limit", "10"}} {
				found := false
				for _, row := range h.run(a.StoreDir(), "", args...)["messages"].([]any) {
					m := row.(map[string]any)
					if m["MsgID"] == "m000" {
						found = true
						if m["DeliveredTo"] != float64(1) || m["ReadBy"] != float64(1) {
							t.Fatalf("receipt counts lost through CLI %v: %v", args, m)
						}
					}
				}
				if !found {
					t.Fatalf("receipt-bearing message missing from %v", args)
				}
			}
			if err = writer.UpsertMessage(store.UpsertMessageParams{ChatJID: "123@s.whatsapp.net", MsgID: "after-upgrade", Timestamp: time.Unix(1, 0), Text: "new message"}); err != nil {
				t.Fatal(err)
			}
			writer.Close()
			changes := retrievalChanges(h.run(a.StoreDir(), "", "messages", "changes", "--since", checkpoint))
			if len(changes) != 1 || changes[0]["msg_id"] != "after-upgrade" || changes[0]["operation"] != "upsert" {
				t.Fatalf("upgrade lost or duplicated journal trigger: %v", changes)
			}
			db, err := sql.Open("sqlite3", filepath.Join(a.StoreDir(), "wacli.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			for version, name := range map[int]string{29: "message receipts", 30: "retrieval identity, revision and sync state", 31: "content-free message change journal"} {
				var got string
				if err = db.QueryRow(`SELECT name FROM schema_migrations WHERE version=?`, version).Scan(&got); err != nil || got != name {
					t.Fatalf("migration %d owned by %q, want %q: %v", version, got, name, err)
				}
			}
			db.Close()
			before = retrievalFingerprint(t, a.StoreDir())
			h.run(a.StoreDir(), "", "messages", "changes", "--since", checkpoint)
			h.run(a.StoreDir(), "", "messages", "list")
			if !reflect.DeepEqual(before, retrievalFingerprint(t, a.StoreDir())) {
				t.Fatal("post-upgrade read-only CLI changed the archive")
			}
		})
	}
}

func TestRetrievalStage2CLI(t *testing.T) {
	a := newTestApp(t)
	retrievalSeed(t, a, 7)
	h := newRetrievalCLI(t, a)
	retrievalUpgradeFixtures(t, h)
	h.run(a.StoreDir(), "--since", "messages", "changes")
	initial := h.run(a.StoreDir(), "", "messages", "changes", "--checkpoint")
	checkpoint := initial["next_checkpoint"].(string)
	if id, ok := initial["store_id"].(string); !ok || id == "" {
		t.Fatal("missing store identity for deduplication")
	}
	first := h.run(a.StoreDir(), "", "messages", "changes", "--since", "beginning", "--limit", "2")
	if len(retrievalChanges(first)) != 2 || first["has_more"] != true {
		t.Fatal("baseline page")
	}
	retry := h.run(a.StoreDir(), "", "messages", "changes", "--since", "beginning", "--limit", "2")
	if !reflect.DeepEqual(first, retry) {
		t.Fatal("failed consumer retry differs")
	}
	retrievalSyncEvents(t, a, retrievalLive("late", "secret body", 100))
	token := first["next_checkpoint"].(string)
	seen := len(retrievalChanges(first))
	for i := 0; i < 10; i++ {
		d := h.run(a.StoreDir(), "", "messages", "changes", "--since", token, "--limit", "2")
		seen += len(retrievalChanges(d))
		token = d["next_checkpoint"].(string)
		if !d["has_more"].(bool) {
			break
		}
	}
	if seen != 7 {
		t.Fatalf("stable boundary returned %d baseline changes", seen)
	}
	late := h.run(a.StoreDir(), "", "messages", "changes", "--since", token)
	if len(retrievalChanges(late)) != 1 || retrievalChanges(late)[0]["msg_id"] != "late" {
		t.Fatal("late message missing")
	}
	checkpoint = late["next_checkpoint"].(string)
	retrievalSyncEvents(t, a, retrievalLive("late", "secret body", 100))
	if n := len(retrievalChanges(h.run(a.StoreDir(), "", "messages", "changes", "--since", checkpoint))); n != 0 {
		t.Fatalf("identical event made %d changes", n)
	}
	edit := retrievalLive("edit-envelope", "", 200)
	edit.Message = &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(), Key: &waCommon.MessageKey{RemoteJID: proto.String("123@s.whatsapp.net"), ID: proto.String("late")}, EditedMessage: &waE2E.Message{Conversation: proto.String("edited secret")}, TimestampMS: proto.Int64(200000)}}
	revoke := retrievalLive("revoke-envelope", "", 300)
	revoke.Message = &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_REVOKE.Enum(), Key: &waCommon.MessageKey{RemoteJID: proto.String("123@s.whatsapp.net"), ID: proto.String("late")}}}
	retrievalSyncEvents(t, a, edit, revoke)
	changes := retrievalChanges(h.run(a.StoreDir(), "", "messages", "changes", "--since", checkpoint))
	if len(changes) != 2 || changes[0]["operation"] != "upsert" || changes[1]["operation"] != "delete" || changes[0]["sequence"].(float64) >= changes[1]["sequence"].(float64) {
		t.Fatalf("edit/revoke: %v", changes)
	}
	raw, _ := json.Marshal(changes)
	if bytes.Contains(raw, []byte("secret")) {
		t.Fatal("journal exposed content")
	}
	// Fault mode: a caller transaction aborts after the message SQL succeeds.
	// No CLI command exposes that point. Verify both mutation and journal rollback
	// using a real SQLite transaction, then observe through the CLI.
	db, e := sql.Open("sqlite3", filepath.Join(a.StoreDir(), "wacli.db"))
	if e != nil {
		t.Fatal(e)
	}
	before := h.run(a.StoreDir(), "", "messages", "changes", "--checkpoint")["next_checkpoint"].(string)
	tx, e := db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(`UPDATE messages SET text='rollback-secret' WHERE msg_id='m000'`); e != nil {
		t.Fatal(e)
	}
	if e = tx.Rollback(); e != nil {
		t.Fatal(e)
	}
	db.Close()
	if n := len(retrievalChanges(h.run(a.StoreDir(), "", "messages", "changes", "--since", before))); n != 0 {
		t.Fatal("rolled back journal visible")
	}
	m := h.run(a.StoreDir(), "", "messages", "show", "--chat", "123@s.whatsapp.net", "--id", "m000")
	if m["Text"] == "rollback-secret" {
		t.Fatal("rolled back text visible")
	}
	lid := "999@lid"
	if e = a.db.UpsertChat(lid, "dm", "Fixture", time.Unix(1, 0)); e != nil {
		t.Fatal(e)
	}
	if e = a.db.UpsertMessage(store.UpsertMessageParams{ChatJID: lid, MsgID: "move", Text: "move secret", Timestamp: time.Unix(1, 0)}); e != nil {
		t.Fatal(e)
	}
	before = h.run(a.StoreDir(), "", "messages", "changes", "--checkpoint")["next_checkpoint"].(string)
	if e = a.db.MigrateLIDToPN(lid, "123@s.whatsapp.net"); e != nil {
		t.Fatal(e)
	}
	moved := retrievalChanges(h.run(a.StoreDir(), "", "messages", "changes", "--since", before))
	found := false
	for _, c := range moved {
		if c["operation"] == "identity_move" && c["chat_jid"] == lid && c["new_chat_jid"] == "123@s.whatsapp.net" {
			found = true
		}
	}
	if !found {
		t.Fatalf("identity not reconciled: %v", moved)
	}
	before = h.run(a.StoreDir(), "", "messages", "changes", "--checkpoint")["next_checkpoint"].(string)
	prune := newTestApp(t)
	retrievalSeed(t, prune, 1)
	pruneStart := h.run(prune.StoreDir(), "", "messages", "changes", "--checkpoint")["next_checkpoint"].(string)
	h.write(prune.StoreDir(), "chats", "cleanup", "--jid", "123@s.whatsapp.net", "--confirm")
	removedPrune := retrievalChanges(h.run(prune.StoreDir(), "", "messages", "changes", "--since", pruneStart))
	if len(removedPrune) != 1 || removedPrune[0]["operation"] != "delete" {
		t.Fatalf("prune: %v", removedPrune)
	}
	retrievalSQL(t, a.StoreDir(), `DELETE FROM messages WHERE msg_id='m000'`)
	removed := retrievalChanges(h.run(a.StoreDir(), "", "messages", "changes", "--since", before))
	if len(removed) != 1 || removed[0]["operation"] != "delete" {
		t.Fatal("local removal")
	}
	before = h.run(a.StoreDir(), "", "messages", "changes", "--checkpoint")["next_checkpoint"].(string)
	h.write(a.StoreDir(), "messages", "purge", "--chat", "123@s.whatsapp.net", "--id", "late", "--confirm")
	h.run(a.StoreDir(), "expired", "messages", "changes", "--since", "beginning")
	purged := retrievalChanges(h.run(a.StoreDir(), "", "messages", "changes", "--since", before))
	if len(purged) == 0 || purged[len(purged)-1]["reason"] != "hard_purge" {
		t.Fatal("purge reason")
	}
	// Simulate age through recorded timestamps, then invoke a normal writable open.
	expired := newTestApp(t)
	retrievalSeed(t, expired, 3)
	part := h.run(expired.StoreDir(), "", "messages", "changes", "--since", "beginning", "--limit", "1")["next_checkpoint"].(string)
	retrievalSQL(t, expired.StoreDir(), `UPDATE message_changes SET recorded_at=1`)
	expired.Close()
	writer, e := store.Open(filepath.Join(expired.StoreDir(), "wacli.db"))
	if e != nil {
		t.Fatal(e)
	}
	writer.Close()
	h.run(expired.StoreDir(), "expired", "messages", "changes", "--since", part)
	expiredStart := h.run(expired.StoreDir(), "", "messages", "changes", "--checkpoint")["next_checkpoint"].(string)
	writer, e = store.Open(filepath.Join(expired.StoreDir(), "wacli.db"))
	if e != nil {
		t.Fatal(e)
	}
	if e = writer.UpsertMessage(store.UpsertMessageParams{ChatJID: "123@s.whatsapp.net", MsgID: "after-expiry", Timestamp: time.Unix(1, 0), Text: "after expiry"}); e != nil {
		t.Fatal(e)
	}
	writer.Close()
	afterExpiry := retrievalChanges(h.run(expired.StoreDir(), "", "messages", "changes", "--since", expiredStart))
	if len(afterExpiry) != 1 || afterExpiry[0]["sequence"].(float64) <= 3 {
		t.Fatalf("high water reused: %v", afterExpiry)
	}
	h.run(a.StoreDir(), "checkpoint", "messages", "changes", "--since", "bad")
	h.run(expired.StoreDir(), "store", "messages", "changes", "--since", checkpoint)
	a.Close()
	fingerprint := retrievalFingerprint(t, a.StoreDir())
	h.run(a.StoreDir(), "", "messages", "changes", "--checkpoint")
	h.run(a.StoreDir(), "", "messages", "changes", "--since", before)
	if !reflect.DeepEqual(fingerprint, retrievalFingerprint(t, a.StoreDir())) {
		t.Fatal("checkpoint read writes")
	}
	// Delete-for-me and history use real event ingestion, independent of message time.
	own := newTestApp(t)
	ownStart := h.run(own.StoreDir(), "", "messages", "changes", "--checkpoint")["next_checkpoint"].(string)
	retrievalSyncEvents(t, own, historySyncWithTextMessages(types.NewJID("123", types.DefaultUserServer), time.Unix(1, 0), "history"),
		&events.DeleteForMe{ChatJID: types.NewJID("123", types.DefaultUserServer), MessageID: "history", Timestamp: time.Unix(2, 0)})
	ownChanges := retrievalChanges(h.run(own.StoreDir(), "", "messages", "changes", "--since", ownStart))
	if len(ownChanges) != 2 || ownChanges[1]["reason"] != store.MessageDeletionReasonWhatsAppDeleteForMe {
		t.Fatalf("history/delete-for-me: %v", ownChanges)
	}
	// Migration seeds retained rows, including tombstones, without historical edits.
	legacy := newTestApp(t)
	retrievalSeed(t, legacy, 4)
	legacy.Close()
	retrievalSQL(t, legacy.StoreDir(), `DROP TABLE message_changes;DROP TABLE change_state;DELETE FROM schema_migrations WHERE version=31`)
	writer, e = store.Open(filepath.Join(legacy.StoreDir(), "wacli.db"))
	if e != nil {
		t.Fatal(e)
	}
	writer.Close()
	seed := retrievalChanges(h.run(legacy.StoreDir(), "", "messages", "changes", "--since", "beginning"))
	if len(seed) != 4 {
		t.Fatalf("migration seed %v", seed)
	}
	for _, c := range seed {
		if c["operation"] != "upsert" {
			t.Fatal("invented historic mutation")
		}
	}
	capped := newTestApp(t)
	capped.Close()
	db, e = sql.Open("sqlite3", filepath.Join(capped.StoreDir(), "wacli.db"))
	if e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"retrieval_messages_insert", "retrieval_messages_update", "retrieval_messages_delete", "changes_payload_purge"} {
		if _, e = db.Exec("DROP TRIGGER " + name); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = db.Exec(`DROP TABLE message_changes; DROP TABLE change_state;DELETE FROM schema_migrations WHERE version=31;
 INSERT INTO chats(jid,kind) VALUES('123@s.whatsapp.net','dm');
 WITH RECURSIVE nums(n) AS(SELECT 1 UNION ALL SELECT n+1 FROM nums WHERE n<100001)
 INSERT INTO messages(chat_jid,msg_id,ts,from_me,text) SELECT '123@s.whatsapp.net',printf('cap%d',n),1,0,'cap secret' FROM nums;`); e != nil {
		t.Fatal(e)
	}
	db.Close()
	writer, e = store.Open(filepath.Join(capped.StoreDir(), "wacli.db"))
	if e != nil {
		t.Fatal(e)
	}
	writer.Close()
	h.run(capped.StoreDir(), "expired", "messages", "changes", "--since", "beginning")
	db, e = sql.Open("sqlite3", filepath.Join(capped.StoreDir(), "wacli.db"))
	if e != nil {
		t.Fatal(e)
	}
	var count int
	if e = db.QueryRow(`SELECT count(*) FROM message_changes`).Scan(&count); e != nil {
		t.Fatal(e)
	}
	if count != 100000 {
		t.Fatalf("retention count: %d", count)
	}
	var payloadColumns int
	if e = db.QueryRow(`SELECT count(*) FROM pragma_table_info('message_changes') WHERE name IN ('text','media_key','media_caption','display_text','filename')`).Scan(&payloadColumns); e != nil {
		t.Fatal(e)
	}
	db.Close()
	if payloadColumns != 0 {
		t.Fatal("journal payload schema")
	}

}

func TestRetrievalStage3CLI(t *testing.T) {
	a := newTestApp(t)
	retrievalSeed(t, a, 12)
	h := newRetrievalCLI(t, a)
	if e := a.db.UpsertContact("123@s.whatsapp.net", "", "", "Ada", "", ""); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"--chat", "--from"} {
		d := h.run(a.StoreDir(), "", "messages", "search", "needle", name, "Ada", "--limit", "2", "--context-before", "2", "--context-after", "1")
		if len(retrievalIDs(d)) != 2 {
			t.Fatal("name search")
		}
	}
	named := h.run(a.StoreDir(), "", "messages", "list", "--sender", "Ada", "--limit", "3")
	jid := h.run(a.StoreDir(), "", "messages", "list", "--sender", "123@s.whatsapp.net", "--limit", "3")
	if !reflect.DeepEqual(retrievalIDs(named), retrievalIDs(jid)) {
		t.Fatal("sender name resolution differs")
	}
	if e := a.db.UpsertContact("456@s.whatsapp.net", "", "", "Ada", "", ""); e != nil {
		t.Fatal(e)
	}
	amb := h.run(a.StoreDir(), "ambiguous", "messages", "search", "needle", "--chat", "Ada")
	errText := fmt.Sprint(amb["error"])
	if !strings.Contains(errText, "123@s.whatsapp.net") || !strings.Contains(errText, "456@s.whatsapp.net") {
		t.Fatal("missing candidates")
	}
	h.run(a.StoreDir(), "no local", "messages", "list", "--chat", "Unknown")
	h.run(a.StoreDir(), "", "messages", "search", "needle", "--chat", "123@s.whatsapp.net")
	if e := a.db.UpdateMessageText("123@s.whatsapp.net", "m005", "unique hit"); e != nil {
		t.Fatal(e)
	}
	if e := a.db.MarkMessageDeletedForMe("123@s.whatsapp.net", "m004", "", false, time.Unix(1700000010, 0)); e != nil {
		t.Fatal(e)
	}
	// Same-timestamp context excludes deleted content and other conversations.
	if e := a.db.UpsertChat("456@s.whatsapp.net", "dm", "Other", time.Unix(1700000000, 0)); e != nil {
		t.Fatal(e)
	}
	if e := a.db.UpsertMessage(store.UpsertMessageParams{ChatJID: "456@s.whatsapp.net", MsgID: "foreign", Timestamp: time.Unix(1700000000, 0), Text: "foreign secret"}); e != nil {
		t.Fatal(e)
	}
	d := h.run(a.StoreDir(), "", "messages", "search", "unique", "--context-before", "2", "--context-after", "1")
	if !reflect.DeepEqual(retrievalIDs(d), []string{"m005"}) {
		t.Fatal("hits changed by context")
	}
	contexts, ok := d["context"].([]any)
	if !ok || len(contexts) != 1 {
		t.Fatal("context missing")
	}
	item := contexts[0].(map[string]any)
	if item["chat_jid"] != "123@s.whatsapp.net" || item["msg_id"] != "m005" {
		t.Fatal("context identity")
	}
	want := []string{"m002", "m003", "m005", "m006"}
	if got := retrievalIDs(map[string]any{"messages": item["messages"]}); !reflect.DeepEqual(got, want) {
		t.Fatalf("context: %v", got)
	}
	// Filters choose hits only; a hit after the cutoff has earlier context.
	if e := a.db.UpdateMessageText("123@s.whatsapp.net", "m009", "timehit"); e != nil {
		t.Fatal(e)
	}
	d = h.run(a.StoreDir(), "", "messages", "search", "timehit", "--after", "2023-11-14T22:13:20Z", "--context-before", "1")
	item = d["context"].([]any)[0].(map[string]any)
	if got := retrievalIDs(map[string]any{"messages": item["messages"]}); !reflect.DeepEqual(got, []string{"m008", "m009"}) {
		t.Fatalf("filter context: %v", got)
	}
	hits := h.run(a.StoreDir(), "", "messages", "search", "needle", "--limit", "2")
	rich := h.run(a.StoreDir(), "", "messages", "search", "needle", "--limit", "2", "--context-before", "1")
	if !reflect.DeepEqual(retrievalIDs(hits), retrievalIDs(rich)) || !reflect.DeepEqual(hits["page"], rich["page"]) {
		t.Fatal("context affects hit pagination")
	}
	h.run(a.StoreDir(), "context", "messages", "search", "needle", "--context-before", "21")
	// Phone/LID equivalence must collect one chronological conversation.
	lid := "999@lid"
	if e := a.db.UpsertChat(lid, "dm", "Old identity", time.Unix(1700000000, 0)); e != nil {
		t.Fatal(e)
	}
	if e := a.db.UpsertMessage(store.UpsertMessageParams{ChatJID: lid, MsgID: "lid-neighbor", Timestamp: time.Unix(1700000000, 0), Text: "neighbor"}); e != nil {
		t.Fatal(e)
	}
	writeSessionLIDMap(t, filepath.Join(a.StoreDir(), "session.db"), "999", "123")
	d = h.run(a.StoreDir(), "", "messages", "search", "timehit", "--context-before", "1")
	item = d["context"].([]any)[0].(map[string]any)
	if got := retrievalIDs(map[string]any{"messages": item["messages"]}); !reflect.DeepEqual(got, []string{"lid-neighbor", "m009"}) {
		t.Fatalf("identity context: %v", got)
	}
	c := exec.Command(h.bin, "--store", a.StoreDir(), "--read-only", "messages", "search", "unique", "--context-before", "1")
	b, e := c.CombinedOutput()
	fmt.Fprintf(&h.log, "human context: %s\n", b)
	if e != nil || !bytes.Contains(b, []byte("Context")) {
		t.Fatalf("human context: %s %v", b, e)
	}
}

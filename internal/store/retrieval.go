package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Retrieval metadata is initialized only by writable migrations.
func migrateRetrieval(d *DB) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`CREATE TABLE IF NOT EXISTS retrieval_state (
 id INTEGER PRIMARY KEY CHECK(id=1), store_id TEXT NOT NULL, revision INTEGER NOT NULL DEFAULT 0,
 last_sync_at INTEGER
 ); INSERT OR IGNORE INTO retrieval_state(id,store_id) VALUES(1,lower(hex(randomblob(16))));`); err != nil {
		return err
	}
	for _, table := range []string{"messages", "chats", "contacts", "contact_aliases", "starred", "message_locations"} {
		var n int
		if err = tx.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			continue
		}
		for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
			name := "retrieval_" + table + "_" + strings.ToLower(op)
			if _, err = tx.Exec(`CREATE TRIGGER IF NOT EXISTS ` + name + ` AFTER ` + op + ` ON ` + table + ` BEGIN UPDATE retrieval_state SET revision=revision+1 WHERE id=1; END`); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// Receipt counts are part of exported messages. Add their revision triggers
// separately so archives that already applied retrieval migration 30 acquire
// invalidation without resetting identity or touching message journal state.
func migrateReceiptRetrieval(d *DB) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
		name := "retrieval_message_receipts_" + strings.ToLower(op)
		if _, err = tx.Exec(`CREATE TRIGGER IF NOT EXISTS ` + name + ` AFTER ` + op + ` ON message_receipts BEGIN UPDATE retrieval_state SET revision=revision+1 WHERE id=1; END`); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (d *DB) RecordSuccessfulSync(t time.Time) error {
	_, err := d.sql.Exec(`UPDATE retrieval_state SET last_sync_at=? WHERE id=1`, t.UTC().Unix())
	return err
}
func (d *DB) LastSuccessfulSync() (*time.Time, error) {
	var ts sql.NullInt64
	err := d.sql.QueryRow(`SELECT last_sync_at FROM retrieval_state WHERE id=1`).Scan(&ts)
	if err != nil {
		exists, e := d.tableExists("retrieval_state")
		if e != nil {
			return nil, e
		}
		if !exists {
			return nil, nil
		}
		return nil, err
	}
	if !ts.Valid {
		return nil, nil
	}
	t := fromUnix(ts.Int64)
	return &t, nil
}

type PageInfo struct {
	HasMore    bool    `json:"has_more"`
	NextCursor *string `json:"next_cursor"`
}
type ArchiveSource struct {
	LastSyncAt *time.Time `json:"last_sync_at"`
	Scope      string     `json:"scope"`
}
type MessagePage struct {
	Messages []Message     `json:"messages"`
	FTS      bool          `json:"fts"`
	Page     PageInfo      `json:"page"`
	Source   ArchiveSource `json:"source"`
}
type pageCursor struct {
	Version  int     `json:"v"`
	Store    string  `json:"s"`
	Revision int64   `json:"r"`
	Filters  string  `json:"f"`
	Backend  bool    `json:"b"`
	TS       int64   `json:"t"`
	Row      int64   `json:"i"`
	Score    float64 `json:"k"`
}

func encodeRetrievalToken(v any) string {
	b, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(b)
}
func decodeRetrievalToken(token string, v any) error {
	if len(token) == 0 || len(token) > 2048 {
		return fmt.Errorf("malformed cursor/checkpoint; restart the read")
	}
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return fmt.Errorf("malformed cursor/checkpoint; restart the read")
	}
	if err = json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("malformed cursor/checkpoint; restart the read")
	}
	return nil
}
func pageFilterHash(command string, list ListMessagesParams, search SearchMessagesParams) string {
	list.Limit = 0
	search.Limit = 0
	list.ChatJIDs = uniqueNonEmptyStrings(append(list.ChatJIDs, list.ChatJID))
	list.ChatJID = ""
	sort.Strings(list.ChatJIDs)
	search.ChatJIDs = uniqueNonEmptyStrings(append(search.ChatJIDs, search.ChatJID))
	search.ChatJID = ""
	sort.Strings(search.ChatJIDs)
	// LIKE treats spaces literally; bind the actual query rather than trimming it.
	if list.After != nil {
		t := list.After.UTC()
		list.After = &t
	}
	if list.Before != nil {
		t := list.Before.UTC()
		list.Before = &t
	}
	if search.After != nil {
		t := search.After.UTC()
		search.After = &t
	}
	if search.Before != nil {
		t := search.Before.UTC()
		search.Before = &t
	}
	search.Type = normalizedMessageType(search.Type)
	b, _ := json.Marshal([]any{command, list, search})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (d *DB) ListMessagesPage(command string, p ListMessagesParams, token string) (MessagePage, error) {
	return d.readMessagePage(command, p, SearchMessagesParams{}, token)
}
func (d *DB) SearchMessagesPage(p SearchMessagesParams, token string) (MessagePage, error) {
	return d.readMessagePage("search", ListMessagesParams{}, p, token)
}
func (d *DB) readMessagePage(command string, list ListMessagesParams, search SearchMessagesParams, token string) (MessagePage, error) {
	var out MessagePage
	err := d.ReadView(func(view *DB) error {
		var err error
		out, err = view.readMessagePageView(command, list, search, token)
		return err
	})
	return out, err
}
func (d *DB) readMessagePageView(command string, list ListMessagesParams, search SearchMessagesParams, token string) (MessagePage, error) {
	out := MessagePage{FTS: d.ftsEnabled, Source: ArchiveSource{Scope: "local_archive"}}
	limit := list.Limit
	if command == "search" {
		limit = search.Limit
	}
	if limit <= 0 {
		limit = 50
	}
	if limit == int(^uint(0)>>1) {
		return out, fmt.Errorf("--limit is too large to request a continuation row")
	}
	if command == "search" {
		if strings.TrimSpace(search.Query) == "" {
			return out, fmt.Errorf("query is required")
		}
		typ := normalizedMessageType(search.Type)
		if typ != "" && !validSearchMessageType(typ) {
			return out, fmt.Errorf("unsupported message type %q", search.Type)
		}
		if typ == "text" && search.HasMedia {
			return out, fmt.Errorf("cannot combine has-media with type=text")
		}
	}
	tx := d.readQuery()
	var err error
	var meta pageCursor
	var ts sql.NullInt64
	// The first metadata SELECT establishes the same view used for the rows.
	var exists int
	if err = tx.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='retrieval_state' AND type='table'`).Scan(&exists); err != nil {
		return out, err
	}
	if exists != 0 {
		err = tx.QueryRow(`SELECT store_id,revision,last_sync_at FROM retrieval_state WHERE id=1`).Scan(&meta.Store, &meta.Revision, &ts)
		if err != nil {
			return out, err
		}
	}
	if ts.Valid {
		t := fromUnix(ts.Int64)
		out.Source.LastSyncAt = &t
	}
	meta.Version = 1
	meta.Filters = pageFilterHash(command, list, search)
	meta.Backend = d.ftsEnabled
	var cur pageCursor
	if token != "" {
		if exists == 0 {
			return out, fmt.Errorf("cursor continuation requires a writable store migration; repeat the first-page read afterwards")
		}
		if err = decodeRetrievalToken(token, &cur); err != nil {
			return out, err
		}
		if cur.Version != 1 {
			return out, fmt.Errorf("unsupported cursor version; restart the read")
		}
		if cur.Store != meta.Store {
			return out, fmt.Errorf("cursor belongs to another store; restart the read")
		}
		if cur.Backend != meta.Backend {
			return out, fmt.Errorf("cursor search backend changed; restart the read")
		}
		if cur.Filters != meta.Filters {
			return out, fmt.Errorf("cursor filters differ; repeat the original command and filters")
		}
		if cur.Revision != meta.Revision {
			return out, fmt.Errorf("archive changed since cursor; restart the read")
		}
	}
	var msgs []Message
	if command == "search" {
		search.Limit = limit + 1
		if token != "" {
			search.key = &messagePageKey{ts: cur.TS, row: cur.Row, score: cur.Score}
		}
		msgs, err = d.SearchMessages(search)
	} else {
		list.Limit = limit + 1
		if token != "" {
			list.key = &messagePageKey{ts: cur.TS, row: cur.Row}
		}
		msgs, err = d.ListMessages(list)
	}
	if err != nil {
		return out, err
	}

	out.Page.HasMore = len(msgs) > limit
	if out.Page.HasMore {
		msgs = msgs[:limit]
		if exists != 0 {
			last := msgs[len(msgs)-1]
			meta.TS = last.Timestamp.Unix()
			meta.Row = last.rowID
			meta.Score = last.rank
			next := encodeRetrievalToken(meta)
			out.Page.NextCursor = &next
		}
	}
	out.Messages = msgs
	return out, nil
}

// queryRows is shared by ordinary reads and transactional page reads.
type queryRows interface {
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
}

// ReadView shares a single SQLite snapshot across rows, cursors, names and context.
func (d *DB) ReadView(read func(*DB) error) error {
	if d.readRows != nil {
		return read(d)
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	view := &DB{path: d.path, sql: d.sql, q: d.q.WithTx(tx), ftsEnabled: d.ftsEnabled, receiptsEnabled: d.receiptsEnabled, adReferralEnabled: d.adReferralEnabled, readRows: tx}
	if err = read(view); err != nil {
		return err
	}
	return tx.Commit()
}
func (d *DB) readQuery() queryRows {
	if d.readRows != nil {
		return d.readRows
	}
	return d.sql
}

type messagePageKey struct {
	ts, row int64
	score   float64
}

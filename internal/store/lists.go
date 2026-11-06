package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

func migrateLists(d *DB) error {
	_, err := d.sql.Exec(`
		CREATE TABLE IF NOT EXISTS lists (
			id TEXT PRIMARY KEY, name TEXT, color INTEGER, predefined_id INTEGER,
			type INTEGER, order_index INTEGER, is_active INTEGER, is_immutable INTEGER,
			deleted INTEGER NOT NULL DEFAULT 0, updated_at INTEGER NOT NULL
		);
		CREATE TABLE IF NOT EXISTS chat_lists (
			chat_jid TEXT NOT NULL, list_id TEXT NOT NULL,
			labeled INTEGER NOT NULL, updated_at INTEGER NOT NULL,
			PRIMARY KEY(chat_jid, list_id)
		);
		CREATE INDEX IF NOT EXISTS idx_chat_lists_list ON chat_lists(list_id, labeled, chat_jid);
		CREATE TABLE IF NOT EXISTS list_sync_state (
			id INTEGER PRIMARY KEY CHECK(id=1), full_sync_events_seen INTEGER NOT NULL DEFAULT 0
		);
		INSERT OR IGNORE INTO list_sync_state(id) VALUES(1);
	`)
	return err
}

type List struct {
	ID                       string `json:"id"`
	Name                     string `json:"name"`
	Color                    *int32 `json:"color"`
	PredefinedID             *int32 `json:"predefined_id"`
	Type                     *int32 `json:"type"`
	OrderIndex               *int32 `json:"order_index"`
	IsActive                 *bool  `json:"is_active"`
	IsImmutable              *bool  `json:"is_immutable"`
	ChatMembershipsSupported bool   `json:"chat_memberships_supported"`
}

// ListUpdate preserves absent optional fields, including on deletion events.
type ListUpdate struct {
	ID                    string
	Name                  *string
	Color, PredefinedID   *int32
	Type, OrderIndex      *int32
	IsActive, IsImmutable *bool
	Deleted               *bool
	Timestamp             time.Time
	FromFullSync          bool
}

type ListsResult struct {
	Lists              []List `json:"lists"`
	FullSyncEventsSeen bool   `json:"full_sync_events_seen"`
	Completeness       string `json:"completeness"`
}

func recordListFullSync(tx *sql.Tx, full bool) error {
	if !full {
		return nil
	}
	_, err := tx.Exec(`UPDATE list_sync_state SET full_sync_events_seen=1 WHERE id=1`)
	return err
}

func (d *DB) UpsertList(p ListUpdate) error {
	if strings.TrimSpace(p.ID) == "" {
		return fmt.Errorf("list ID is required")
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.Exec(`INSERT INTO lists(id,name,color,predefined_id,type,order_index,is_active,is_immutable,deleted,updated_at)
		VALUES(?,?,?,?,?,?,?,?,COALESCE(?,0),?)
		ON CONFLICT(id) DO UPDATE SET
			name=COALESCE(excluded.name,lists.name), color=COALESCE(excluded.color,lists.color),
			predefined_id=COALESCE(excluded.predefined_id,lists.predefined_id),
			type=COALESCE(excluded.type,lists.type), order_index=COALESCE(excluded.order_index,lists.order_index),
			is_active=COALESCE(excluded.is_active,lists.is_active),
			is_immutable=COALESCE(excluded.is_immutable,lists.is_immutable),
			deleted=COALESCE(?,lists.deleted), updated_at=excluded.updated_at
		WHERE excluded.updated_at > lists.updated_at
			OR (excluded.updated_at = lists.updated_at AND lists.deleted <= excluded.deleted)`,
		p.ID, p.Name, p.Color, p.PredefinedID, p.Type, p.OrderIndex, p.IsActive, p.IsImmutable,
		p.Deleted, p.Timestamp.UnixMilli(), p.Deleted)
	if err != nil {
		return err
	}
	if err := recordListFullSync(tx, p.FromFullSync); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) SetChatList(chat, id string, labeled bool, at time.Time, fullSync bool) error {
	if strings.TrimSpace(chat) == "" || strings.TrimSpace(id) == "" {
		return fmt.Errorf("chat JID and list ID are required")
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.Exec(`INSERT INTO chat_lists(chat_jid,list_id,labeled,updated_at) VALUES(?,?,?,?)
		ON CONFLICT(chat_jid,list_id) DO UPDATE SET labeled=excluded.labeled, updated_at=excluded.updated_at
		WHERE excluded.updated_at > chat_lists.updated_at
			OR (excluded.updated_at = chat_lists.updated_at AND excluded.labeled < chat_lists.labeled)`,
		chat, id, labeled, at.UnixMilli())
	if err != nil {
		return err
	}
	if err := recordListFullSync(tx, fullSync); err != nil {
		return err
	}
	return tx.Commit()
}

// Lists returns observed definitions, not a claim that the phone's collection
// is complete. A full-sync event alone cannot prove a verified empty snapshot.
func (d *DB) Lists() (ListsResult, error) {
	result := ListsResult{Lists: []List{}, Completeness: "unknown"}
	// Read-only opens never migrate, so tolerate a missing or half-created schema.
	for _, table := range []string{"lists", "list_sync_state"} {
		exists, err := d.tableExists(table)
		if err != nil || !exists {
			return result, err
		}
	}
	if err := d.sql.QueryRow(`SELECT full_sync_events_seen FROM list_sync_state WHERE id=1`).Scan(&result.FullSyncEventsSeen); err != nil {
		return result, err
	}
	rows, err := d.sql.Query(`SELECT id,COALESCE(name,''),color,predefined_id,type,order_index,is_active,is_immutable
		FROM lists WHERE deleted=0 ORDER BY COALESCE(order_index,0),name,id`)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var l List
		if err := rows.Scan(&l.ID, &l.Name, &l.Color, &l.PredefinedID, &l.Type, &l.OrderIndex, &l.IsActive, &l.IsImmutable); err != nil {
			return result, err
		}
		l.ChatMembershipsSupported = l.Type == nil || *l.Type == 0 || *l.Type == 4 || *l.Type == 5
		result.Lists = append(result.Lists, l)
	}
	return result, rows.Err()
}

// ResolveList accepts an exact ID first, then an exact, case-sensitive name.
// Duplicate names require an ID; system lists must not look like empty lists.
func (d *DB) ResolveList(value string) (string, error) {
	result, err := d.Lists()
	if err != nil {
		return "", err
	}
	// Lists hides tombstones. Check them separately, retaining ID precedence
	// and allowing an active definition to reuse a deleted definition's name.
	exists, err := d.tableExists("lists")
	if err != nil {
		return "", err
	}
	if exists {
		var deleted bool
		err := d.sql.QueryRow(`SELECT deleted FROM lists WHERE id=? OR name=?
			ORDER BY (id=?) DESC,deleted ASC LIMIT 1`, value, value, value).Scan(&deleted)
		if err != nil && err != sql.ErrNoRows {
			return "", err
		}
		if deleted {
			return "", fmt.Errorf("list %q was deleted on the phone", value)
		}
	}
	var matches []List
	for _, l := range result.Lists {
		if l.ID == value {
			matches = []List{l}
			break
		}
		if l.Name == value {
			matches = append(matches, l)
		}
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("list %q is not in the local index; sync first", value)
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("list name %q is ambiguous; use an ID", value)
	}
	l := matches[0]
	// NONE/absent is used by older Business labels; PREDEFINED and CUSTOM
	// carry explicit memberships. Computed personal filters are not supported.
	if !l.ChatMembershipsSupported {
		return "", fmt.Errorf("list %q has unsupported membership type %d (favorites/system lists are out of scope)", value, *l.Type)
	}
	return l.ID, nil
}

func migrateLIDListsToPN(tx *sql.Tx, lid, pn string) error {
	if _, err := tx.Exec(`INSERT INTO chat_lists(chat_jid,list_id,labeled,updated_at)
		SELECT ?,list_id,labeled,updated_at FROM chat_lists WHERE chat_jid=?
		ON CONFLICT(chat_jid,list_id) DO UPDATE SET labeled=excluded.labeled,updated_at=excluded.updated_at
		WHERE excluded.updated_at > chat_lists.updated_at
			OR (excluded.updated_at = chat_lists.updated_at AND excluded.labeled < chat_lists.labeled)`, pn, lid); err != nil {
		return err
	}
	_, err := tx.Exec(`DELETE FROM chat_lists WHERE chat_jid=?`, lid)
	return err
}

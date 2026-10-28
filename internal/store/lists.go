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
	if err != nil {
		return err
	}
	// Keep the migration idempotent even when row 36 was already claimed by
	// the Lists prerequisite or another build. Existing rows are not rewritten.
	for _, col := range []struct{ table, name, ddl string }{
		{"lists", "mute_end_time_ms", "ALTER TABLE lists ADD COLUMN mute_end_time_ms INTEGER"},
		{"chat_lists", "applied_order", "ALTER TABLE chat_lists ADD COLUMN applied_order INTEGER NOT NULL DEFAULT 0"},
	} {
		has, err := d.tableHasColumn(col.table, col.name)
		if err != nil {
			return err
		}
		if !has {
			if _, err := d.sql.Exec(col.ddl); err != nil {
				return err
			}
		}
	}
	_, err = d.sql.Exec(`CREATE INDEX IF NOT EXISTS idx_chat_lists_applied_order ON chat_lists(applied_order)`)
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
	MuteEndTimeMS            *int64 `json:"mute_end_time_ms"`
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
	MuteEndTimeMS         *int64
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

// UpsertList applies the persistence owner's ordered action sequence. Device
// timestamps are data, not an arbitration clock (as for contact app-state SETs).
func (d *DB) UpsertList(p ListUpdate) error {
	if strings.TrimSpace(p.ID) == "" {
		return fmt.Errorf("list ID is required")
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.Exec(`INSERT INTO lists(id,name,color,predefined_id,type,order_index,is_active,is_immutable,deleted,updated_at,mute_end_time_ms)
		VALUES(?,?,?,?,?,?,?,?,COALESCE(?,0),?,?)
		ON CONFLICT(id) DO UPDATE SET
			name=COALESCE(excluded.name,lists.name), color=COALESCE(excluded.color,lists.color),
			predefined_id=COALESCE(excluded.predefined_id,lists.predefined_id),
			type=COALESCE(excluded.type,lists.type), order_index=COALESCE(excluded.order_index,lists.order_index),
			is_active=COALESCE(excluded.is_active,lists.is_active),
			is_immutable=COALESCE(excluded.is_immutable,lists.is_immutable),
			mute_end_time_ms=COALESCE(excluded.mute_end_time_ms,lists.mute_end_time_ms),
			deleted=COALESCE(?,lists.deleted), updated_at=excluded.updated_at`,
		p.ID, p.Name, p.Color, p.PredefinedID, p.Type, p.OrderIndex, p.IsActive, p.IsImmutable,
		p.Deleted, p.Timestamp.UnixMilli(), p.MuteEndTimeMS, p.Deleted)
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
	// Persist application order across raw LID/PN aliases for a later merge.
	// Legacy rows start at zero: their device timestamps cannot establish order.
	_, err = tx.Exec(`INSERT INTO chat_lists(chat_jid,list_id,labeled,updated_at,applied_order)
		VALUES(?,?,?,?,(SELECT COALESCE(MAX(applied_order),0)+1 FROM chat_lists))
		ON CONFLICT(chat_jid,list_id) DO UPDATE SET labeled=excluded.labeled, updated_at=excluded.updated_at,
			applied_order=excluded.applied_order`,
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
	muteColumn, err := d.listMuteEndTimeColumn()
	if err != nil {
		return result, err
	}
	rows, err := d.sql.Query(`SELECT id,COALESCE(name,''),color,predefined_id,type,order_index,is_active,is_immutable,` + muteColumn + `
		FROM lists WHERE deleted=0 ORDER BY COALESCE(order_index,0),name,id`)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var l List
		if err := rows.Scan(&l.ID, &l.Name, &l.Color, &l.PredefinedID, &l.Type, &l.OrderIndex, &l.IsActive, &l.IsImmutable, &l.MuteEndTimeMS); err != nil {
			return result, err
		}
		l.ChatMembershipsSupported = listTypeHasChatMemberships(l.Type)
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
	if _, err := tx.Exec(`INSERT INTO chat_lists(chat_jid,list_id,labeled,updated_at,applied_order)
		SELECT ?,list_id,labeled,updated_at,applied_order FROM chat_lists WHERE chat_jid=?
		ON CONFLICT(chat_jid,list_id) DO UPDATE SET labeled=excluded.labeled,updated_at=excluded.updated_at,applied_order=excluded.applied_order
		WHERE excluded.applied_order > chat_lists.applied_order
			OR (excluded.applied_order = chat_lists.applied_order AND excluded.labeled < chat_lists.labeled)`, pn, lid); err != nil {
		return err
	}
	_, err := tx.Exec(`DELETE FROM chat_lists WHERE chat_jid=?`, lid)
	return err
}

// ListRecord is a stored definition, including a deleted one.
type ListRecord struct {
	List
	Deleted bool `json:"deleted"`
}

// ListByID returns a stored definition, including a deleted one.
func (d *DB) ListByID(id string) (ListRecord, bool, error) {
	exists, err := d.tableExists("lists")
	if err != nil || !exists {
		return ListRecord{}, false, err
	}
	var r ListRecord
	muteColumn, err := d.listMuteEndTimeColumn()
	if err != nil {
		return r, false, err
	}
	err = d.sql.QueryRow(`SELECT id,COALESCE(name,''),color,predefined_id,type,order_index,is_active,is_immutable,deleted,`+muteColumn+`
		FROM lists WHERE id=?`, id).Scan(&r.ID, &r.Name, &r.Color, &r.PredefinedID, &r.Type, &r.OrderIndex, &r.IsActive, &r.IsImmutable, &r.Deleted, &r.MuteEndTimeMS)
	if err == sql.ErrNoRows {
		return ListRecord{}, false, nil
	}
	if err != nil {
		return ListRecord{}, false, err
	}
	r.ChatMembershipsSupported = listTypeHasChatMemberships(r.Type)
	return r, true, nil
}

// Read-only opens of prerequisite Lists stores cannot add the optional column.
func (d *DB) listMuteEndTimeColumn() (string, error) {
	has, err := d.tableHasColumn("lists", "mute_end_time_ms")
	if err != nil {
		return "", err
	}
	if has {
		return "mute_end_time_ms", nil
	}
	return "NULL", nil
}

// MaxNumericListID returns the largest numeric ID ever observed, deleted
// definitions included, so a new label never reuses a retired ID.
func (d *DB) MaxNumericListID() (int64, error) {
	exists, err := d.tableExists("lists")
	if err != nil || !exists {
		return 0, err
	}
	var maxID sql.NullInt64
	err = d.sql.QueryRow(`SELECT MAX(CAST(id AS INTEGER)) FROM lists WHERE id GLOB '[0-9]*' AND id NOT GLOB '*[^0-9]*'`).Scan(&maxID)
	return maxID.Int64, err
}

// ResolveListForWrite resolves an exact ID, then an exact unique name, among
// live definitions. Unlike ResolveList it accepts every list type; callers
// decide which writes a type supports.
func (d *DB) ResolveListForWrite(value string) (List, error) {
	result, err := d.Lists()
	if err != nil {
		return List{}, err
	}
	var matches []List
	for _, l := range result.Lists {
		if l.ID == value {
			return l, nil
		}
		if l.Name == value {
			matches = append(matches, l)
		}
	}
	if len(matches) == 0 {
		return List{}, fmt.Errorf("list %q is not in the local index; sync first", value)
	}
	if len(matches) > 1 {
		return List{}, fmt.Errorf("list name %q is ambiguous; use an ID", value)
	}
	return matches[0], nil
}

func listTypeHasChatMemberships(t *int32) bool {
	return t == nil || *t == 0 || *t == 4 || *t == 5
}

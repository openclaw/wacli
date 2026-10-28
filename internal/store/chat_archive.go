package store

import (
	"database/sql"
	"fmt"
	"time"
)

func migrateChatAutoUnarchive(d *DB) error {
	hasChats, err := d.tableExists("chats")
	if err != nil || !hasChats {
		return err
	}
	for _, column := range []string{"archive_action_ms", "archive_incoming_ms", "archive_cleared_ms", "archive_setting_min_ms"} {
		exists, err := d.tableHasColumn("chats", column)
		if err != nil {
			return err
		}
		if !exists {
			if _, err := d.sql.Exec("ALTER TABLE chats ADD COLUMN " + column + " INTEGER NOT NULL DEFAULT 0"); err != nil {
				return fmt.Errorf("add chat archive boundary: %w", err)
			}
		}
	}
	_, err = d.sql.Exec(`CREATE TABLE IF NOT EXISTS chat_archive_setting (
 id INTEGER PRIMARY KEY CHECK(id=1), enabled INTEGER NOT NULL, timestamp_ms INTEGER NOT NULL
)`)
	return err
}

// Missing preferences and archive timestamps are unknown, never the default
// phone preference. A full regular_low replay supplies them on upgraded stores.
func (d *DB) ChatArchiveRecoveryRequired() (bool, error) {
	var required bool
	err := d.sql.QueryRow(`SELECT NOT EXISTS(SELECT 1 FROM chat_archive_setting WHERE id=1)
 OR EXISTS(SELECT 1 FROM chats WHERE archived != 0 AND archive_action_ms=0)`).Scan(&required)
	return required, err
}

// SettleChatArchiveRecovery normally runs once a complete regular_low snapshot
// (a verified full replay or a primary recovery) has been persisted. Whatever the
// snapshot did not supply is absent from the primary, so another replay cannot
// recover it: an account that never toggled Keep chats archived has no
// preference mutation, and a locally archived chat without an archive action is
// not archived there. Record both so the replay is not requested again on every
// start, without changing what the user sees. The preference stays unknown,
// stored as keep-archived without a timestamp so any real mutation replaces it,
// and the chats stay archived behind a boundary at the snapshot time, so only
// messages newer than the snapshot can unarchive them later. Known values are
// left alone. A known-broken full sync may also settle after a persisted
// incremental fetch, retaining the snapshot debt and these conservative defaults.
func (d *DB) SettleChatArchiveRecovery(at time.Time) error {
	if at.IsZero() {
		return nil
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO chat_archive_setting(id,enabled,timestamp_ms) VALUES(1,0,0)
 ON CONFLICT(id) DO NOTHING`); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE chats SET archive_action_ms=? WHERE archived != 0 AND archive_action_ms=0`, at.UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) SetChatArchiveSetting(enabled bool, timestamp time.Time) error {
	if timestamp.IsZero() {
		return nil
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO chat_archive_setting(id,enabled,timestamp_ms) VALUES(1,?,?)
 ON CONFLICT(id) DO UPDATE SET enabled=excluded.enabled,timestamp_ms=excluded.timestamp_ms
	 WHERE excluded.timestamp_ms >= chat_archive_setting.timestamp_ms`, boolToInt(enabled), timestamp.UnixMilli()); err != nil {
		return err
	}
	if err := reconcileChatArchive(tx, ""); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) ApplyChatArchive(jid string, archived bool, timestamp time.Time) error {
	if timestamp.IsZero() {
		return d.SetChatArchived(jid, archived)
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO chats(jid,kind) VALUES(?,'unknown') ON CONFLICT(jid) DO NOTHING`, jid); err != nil {
		return err
	}
	// A replay at the same timestamp must not re-archive a newer arrival.
	if _, err := tx.Exec(`UPDATE chats SET
 archived=CASE WHEN ? AND ? >= archive_cleared_ms THEN 1 ELSE 0 END,
 pinned=CASE WHEN ? AND ? >= archive_cleared_ms THEN 0 ELSE pinned END, archive_action_ms=?
 WHERE jid=? AND ? >= archive_action_ms`, archived, timestamp.UnixMilli(), archived, timestamp.UnixMilli(), timestamp.UnixMilli(), jid, timestamp.UnixMilli()); err != nil {
		return err
	}
	if err := reconcileChatArchive(tx, jid); err != nil {
		return err
	}
	return tx.Commit()
}

// ReconcileChatArchiveIncoming runs behind an in-flight app-state replay while
// the original message and incoming boundary remain committed atomically.
func (d *DB) ReconcileChatArchiveIncoming(jid, id string, timestamp time.Time) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := recordChatArchiveIncoming(tx, jid, id, timestamp, 0, 0, true); err != nil {
		return err
	}
	return tx.Commit()
}

func recordChatArchiveIncoming(tx *sql.Tx, jid, id string, timestamp time.Time, observedMS, settingObservedMS int64, stored bool) error {
	if timestamp.IsZero() {
		return nil
	}
	// Message stanzas have second precision. Within that second, a first
	// live delivery observed after a known action wins; later actions retain
	// their real millisecond ordering, and duplicate deliveries never enter here.
	ms := timestamp.UnixMilli()
	if _, err := tx.Exec(`UPDATE chats SET archive_incoming_ms=MAX(archive_incoming_ms,?,
 CASE WHEN ?/1000=?/1000 THEN ?+1 ELSE 0 END,
 CASE WHEN archive_action_ms/1000=?/1000 THEN archive_action_ms+1 ELSE 0 END,
 COALESCE((SELECT CASE WHEN timestamp_ms/1000=?/1000 THEN timestamp_ms+1 ELSE 0 END
 FROM chat_archive_setting WHERE id=1),0)),
 archive_setting_min_ms=MAX(archive_setting_min_ms,?) WHERE jid=?
 AND NOT EXISTS(SELECT 1 FROM message_payload_purges WHERE chat_jid=? AND msg_id=?)
 AND COALESCE((SELECT (? OR (COALESCE(display_text,'')='(message)' AND TRIM(COALESCE(text,''))=''
 AND COALESCE(media_type,'')='' AND COALESCE(buttons,'')=''))
 AND from_me=0 AND edited=0 AND revoked=0 AND deleted_for_me=0
 AND deleted_at IS NULL AND payload_purged_at IS NULL
 AND COALESCE(reaction_to_id,'')='' AND COALESCE(reaction_emoji,'')=''
 FROM messages WHERE chat_jid=? AND msg_id=?),?)`, ms, observedMS, ms, observedMS, ms, ms, settingObservedMS, jid, jid, id, stored, jid, id, !stored); err != nil {
		return err
	}
	return reconcileChatArchive(tx, jid)
}

func reconcileChatArchive(tx *sql.Tx, jid string) error {
	query := `UPDATE chats SET archived=0,
 archive_cleared_ms=MAX(archive_cleared_ms,archive_incoming_ms)
 WHERE archive_action_ms > 0
 AND archive_incoming_ms > archive_action_ms
 AND EXISTS(SELECT 1 FROM chat_archive_setting WHERE id=1 AND enabled=1
   AND timestamp_ms >= archive_setting_min_ms
   AND archive_incoming_ms > timestamp_ms)`
	var args []any
	if jid != "" {
		query += " AND jid=?"
		args = append(args, jid)
	}
	_, err := tx.Exec(query, args...)
	return err
}

func migrateLIDChatArchive(tx *sql.Tx, lid, phone string) error {
	if _, err := tx.Exec(`UPDATE chats AS dst SET
 archived=CASE WHEN src.archive_action_ms > dst.archive_action_ms THEN src.archived
   WHEN dst.archive_action_ms=0 THEN MAX(src.archived,dst.archived) ELSE dst.archived END,
 archive_action_ms=MAX(src.archive_action_ms,dst.archive_action_ms),
 archive_incoming_ms=MAX(src.archive_incoming_ms,dst.archive_incoming_ms),
 archive_setting_min_ms=MAX(src.archive_setting_min_ms,dst.archive_setting_min_ms),
 archive_cleared_ms=MAX(src.archive_cleared_ms,dst.archive_cleared_ms)
 FROM chats AS src WHERE src.jid=? AND dst.jid=?`, lid, phone); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE chats SET archived=0 WHERE jid=? AND archive_action_ms > 0 AND archive_cleared_ms > archive_action_ms`, phone); err != nil {
		return err
	}
	return reconcileChatArchive(tx, phone)
}

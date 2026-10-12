package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const changeRetentionSeconds = 30 * 24 * 60 * 60

const changeCleanupSQL = `DELETE FROM message_changes WHERE recorded_at <= CAST(strftime('%s','now') AS INTEGER)-2592000;
 DELETE FROM message_changes WHERE sequence IN (
 SELECT sequence FROM message_changes ORDER BY sequence LIMIT max(0,(SELECT retained_count-100000 FROM change_state WHERE id=1))
 );`

func migrateChanges(d *DB) error {
	existsMessages, err := d.tableExists("messages")
	if err != nil || !existsMessages {
		return err
	}

	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err = tx.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='change_state'`).Scan(&exists); err != nil {
		return err
	}
	if exists != 0 {
		return tx.Commit()
	}
	if _, err = tx.Exec(`CREATE TABLE change_state(id INTEGER PRIMARY KEY CHECK(id=1), floor INTEGER NOT NULL DEFAULT 0, retained_count INTEGER NOT NULL DEFAULT 0, suppress INTEGER NOT NULL DEFAULT 0);
 INSERT INTO change_state(id) VALUES(1);
 CREATE TABLE message_changes(sequence INTEGER PRIMARY KEY AUTOINCREMENT,recorded_at INTEGER NOT NULL,operation TEXT NOT NULL,chat_jid TEXT NOT NULL,msg_id TEXT NOT NULL,new_chat_jid TEXT,new_msg_id TEXT,reason TEXT NOT NULL DEFAULT '',message_ts INTEGER NOT NULL);
 CREATE INDEX message_changes_recorded ON message_changes(recorded_at);
 INSERT INTO message_changes(recorded_at,operation,chat_jid,msg_id,message_ts)
 SELECT CAST(strftime('%s','now') AS INTEGER),'upsert',chat_jid,msg_id,ts FROM messages ORDER BY rowid;
 UPDATE change_state SET retained_count=(SELECT count(*) FROM message_changes);
 CREATE TRIGGER changes_removed AFTER DELETE ON message_changes BEGIN
 UPDATE change_state SET floor=max(floor,old.sequence),retained_count=retained_count-1 WHERE id=1; END;`); err != nil {
		return err
	}
	if _, err = tx.Exec(changeCleanupSQL); err != nil {
		return err
	}
	if _, err = tx.Exec(`CREATE TRIGGER changes_added AFTER INSERT ON message_changes BEGIN
 UPDATE change_state SET retained_count=retained_count+1 WHERE id=1;` + changeCleanupSQL + `END;`); err != nil {
		return err
	}
	// All durable message columns are meaningful except internal edit ordering and
	// repeat tombstone recording times. Compare actual stored values, not inputs.
	rows, err := tx.Query(`PRAGMA table_info(messages)`)
	if err != nil {
		return err
	}
	var fields []string
	for rows.Next() {
		var cid, nn, pk int
		var name, typ string
		var def sql.NullString
		if err = rows.Scan(&cid, &name, &typ, &nn, &def, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == "edited_ts" || name == "deleted_at" {
			continue
		}
		fields = append(fields, "old."+name+" IS NOT new."+name)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	fields = append(fields, `(old.deleted_at IS NULL) != (new.deleted_at IS NULL)`)
	changed := strings.Join(fields, " OR ")
	for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
		// Reuse the revision trigger; one canonical mutation audit owns both features.
		name := "retrieval_messages_" + strings.ToLower(op)
		if _, err = tx.Exec(`DROP TRIGGER IF EXISTS ` + name); err != nil {
			return err
		}
		row := "new"
		operation := `CASE WHEN new.deleted_at IS NULL THEN 'upsert' ELSE 'delete' END`
		reason := `COALESCE(new.deletion_reason,'')`
		guard := ""
		if op == "UPDATE" {
			guard = " WHEN " + changed
			reason = `CASE WHEN new.payload_purged_at IS NOT NULL THEN 'hard_purge' ELSE COALESCE(new.deletion_reason,'') END`
		}
		if op == "DELETE" {
			row = "old"
			operation = "'delete'"
			reason = `COALESCE(NULLIF(old.deletion_reason,''),'local_removal')`
		}
		body := `UPDATE retrieval_state SET revision=revision+1 WHERE id=1;
   INSERT INTO message_changes(recorded_at,operation,chat_jid,msg_id,reason,message_ts)
   SELECT CAST(strftime('%s','now') AS INTEGER),` + operation + `,` + row + `.chat_jid,` + row + `.msg_id,` + reason + `,` + row + `.ts WHERE (SELECT suppress FROM change_state WHERE id=1)=0;`
		if op == "UPDATE" {
			body = `UPDATE retrieval_state SET revision=revision+1 WHERE id=1;
   INSERT INTO message_changes(recorded_at,operation,chat_jid,msg_id,new_chat_jid,new_msg_id,reason,message_ts)
   SELECT CAST(strftime('%s','now') AS INTEGER),'identity_move',old.chat_jid,old.msg_id,new.chat_jid,new.msg_id,'identity_normalization',new.ts
   WHERE (old.chat_jid IS NOT new.chat_jid OR old.msg_id IS NOT new.msg_id) AND (SELECT suppress FROM change_state WHERE id=1)=0;
   INSERT INTO message_changes(recorded_at,operation,chat_jid,msg_id,reason,message_ts)
   SELECT CAST(strftime('%s','now') AS INTEGER),` + operation + `,new.chat_jid,new.msg_id,` + reason + `,new.ts WHERE (SELECT suppress FROM change_state WHERE id=1)=0 AND old.chat_jid IS new.chat_jid AND old.msg_id IS new.msg_id;`
		}
		if _, err = tx.Exec(`CREATE TRIGGER ` + name + ` AFTER ` + op + ` ON messages` + guard + ` BEGIN ` + body + ` END`); err != nil {
			return err
		}
	}
	// Purge invalidates replay if removing a retained reference makes it incomplete.
	if _, err = tx.Exec(`DROP TRIGGER IF EXISTS changes_payload_purge;
 CREATE TRIGGER changes_payload_purge AFTER INSERT ON message_payload_purges BEGIN
 DELETE FROM message_changes WHERE chat_jid=new.chat_jid AND msg_id=new.msg_id;
 END`); err != nil {
		return err
	}
	return tx.Commit()
}
func (d *DB) cleanupChanges() error {
	exists, err := d.tableExists("change_state")
	if err != nil || !exists {
		return err
	}
	_, err = d.sql.Exec(changeCleanupSQL)
	return err
}

type MessageChange struct {
	Sequence    int64     `json:"sequence"`
	RecordedAt  time.Time `json:"recorded_at"`
	Operation   string    `json:"operation"`
	ChatJID     string    `json:"chat_jid"`
	MsgID       string    `json:"msg_id"`
	NewChatJID  string    `json:"new_chat_jid,omitempty"`
	NewMsgID    string    `json:"new_msg_id,omitempty"`
	Reason      string    `json:"reason,omitempty"`
	MessageTime time.Time `json:"message_time"`
}
type ChangesPage struct {
	StoreID        string          `json:"store_id"`
	Changes        []MessageChange `json:"changes"`
	HasMore        bool            `json:"has_more"`
	NextCheckpoint string          `json:"next_checkpoint"`
}
type changeCheckpoint struct {
	Version  int    `json:"v"`
	Kind     string `json:"k"`
	Store    string `json:"s"`
	Position int64  `json:"p"`
	Boundary *int64 `json:"h,omitempty"`
}

func (d *DB) MessageChanges(since string, checkpoint bool, limit int) (ChangesPage, error) {
	out := ChangesPage{Changes: []MessageChange{}}
	if checkpoint && since != "" {
		return out, fmt.Errorf("--checkpoint and --since are mutually exclusive")
	}
	if !checkpoint && since == "" {
		return out, fmt.Errorf("use --since TOKEN, --since beginning, or --checkpoint")
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 100000 {
		return out, fmt.Errorf("--limit must not exceed 100000")
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var exists int
	if err = tx.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='change_state'`).Scan(&exists); err != nil {
		return out, err
	}
	if exists == 0 {
		return out, fmt.Errorf("changes requires a writable store migration before read-only use")
	}
	var storeID string
	var floor, high int64
	if err = tx.QueryRow(`SELECT store_id FROM retrieval_state WHERE id=1`).Scan(&storeID); err != nil {
		return out, err
	}
	if err = tx.QueryRow(`SELECT max(floor,COALESCE((SELECT max(sequence) FROM message_changes WHERE recorded_at<=?),0)) FROM change_state WHERE id=1`, time.Now().Unix()-changeRetentionSeconds).Scan(&floor); err != nil {
		return out, err
	}
	if err = tx.QueryRow(`SELECT COALESCE((SELECT seq FROM sqlite_sequence WHERE name='message_changes'),0)`).Scan(&high); err != nil {
		return out, err
	}
	out.StoreID = storeID
	token := changeCheckpoint{Version: 1, Kind: "changes", Store: storeID}
	if checkpoint {
		token.Position = high
		out.NextCheckpoint = encodeRetrievalToken(token)
		return out, tx.Commit()
	}
	if since != "beginning" {
		if err = decodeRetrievalToken(since, &token); err != nil {
			return out, err
		}
		if token.Version != 1 || token.Kind != "changes" {
			return out, fmt.Errorf("unsupported checkpoint; use messages changes --checkpoint")
		}
		if token.Store != storeID {
			return out, fmt.Errorf("checkpoint belongs to another store")
		}
		if token.Position < 0 || token.Position > high || (token.Boundary != nil && (*token.Boundary < token.Position || *token.Boundary > high)) {
			return out, fmt.Errorf("invalid checkpoint position")
		}
	}
	if token.Position < floor {
		return out, fmt.Errorf("expired checkpoint: obtain a fresh --checkpoint before a full export, then replay changes after processing that export; restart an invalidated export")
	}
	boundary := high
	if token.Boundary != nil {
		boundary = *token.Boundary
	}
	rows, err := tx.Query(`SELECT sequence,recorded_at,operation,chat_jid,msg_id,COALESCE(new_chat_jid,''),COALESCE(new_msg_id,''),reason,message_ts FROM message_changes WHERE sequence>? AND sequence<=? ORDER BY sequence LIMIT ?`, token.Position, boundary, limit+1)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var c MessageChange
		var recorded, ts int64
		if err = rows.Scan(&c.Sequence, &recorded, &c.Operation, &c.ChatJID, &c.MsgID, &c.NewChatJID, &c.NewMsgID, &c.Reason, &ts); err != nil {
			rows.Close()
			return out, err
		}
		c.RecordedAt = fromUnix(recorded)
		c.MessageTime = fromUnix(ts)
		out.Changes = append(out.Changes, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(out.Changes) > limit {
		out.HasMore = true
		out.Changes = out.Changes[:limit]
		token.Position = out.Changes[limit-1].Sequence
		token.Boundary = &boundary
	} else {
		token.Position = boundary
		token.Boundary = nil
	}
	out.NextCheckpoint = encodeRetrievalToken(token)
	return out, tx.Commit()
}

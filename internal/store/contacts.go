package store

import (
	"fmt"
	"strings"

	"github.com/openclaw/wacli/internal/store/storedb"
)

func (d *DB) SearchContacts(query string, limit int) ([]Contact, error) {
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("query is required")
	}
	if limit <= 0 {
		limit = 50
	}
	q := `
		SELECT c.jid,
		       COALESCE(c.phone,''),
		       COALESCE(NULLIF(a.alias,''), ''),
		       COALESCE(NULLIF(c.system_name,''), ''),
		       COALESCE(NULLIF(a.alias,''), NULLIF(c.system_name,''), NULLIF(c.full_name,''), NULLIF(c.push_name,''), NULLIF(c.business_name,''), NULLIF(c.first_name,''), ''),
		       c.updated_at
		FROM contacts c
		LEFT JOIN contact_aliases a ON a.jid = c.jid
		WHERE LOWER(COALESCE(a.alias,'')) LIKE LOWER(?) ESCAPE '\'
		   OR LOWER(COALESCE(c.system_name,'')) LIKE LOWER(?) ESCAPE '\'
		   OR LOWER(COALESCE(c.full_name,'')) LIKE LOWER(?) ESCAPE '\'
		   OR LOWER(COALESCE(c.push_name,'')) LIKE LOWER(?) ESCAPE '\'
		   OR LOWER(COALESCE(c.first_name,'')) LIKE LOWER(?) ESCAPE '\'
		   OR LOWER(COALESCE(c.business_name,'')) LIKE LOWER(?) ESCAPE '\'
		   OR LOWER(COALESCE(c.phone,'')) LIKE LOWER(?) ESCAPE '\'
		   OR LOWER(c.jid) LIKE LOWER(?) ESCAPE '\'
		ORDER BY COALESCE(NULLIF(a.alias,''), NULLIF(c.system_name,''), NULLIF(c.full_name,''), NULLIF(c.push_name,''), NULLIF(c.business_name,''), NULLIF(c.first_name,''), c.jid)
		LIMIT ?`
	needle := likeContains(query)
	rows, err := d.sql.Query(q, needle, needle, needle, needle, needle, needle, needle, needle, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Contact
	for rows.Next() {
		var c Contact
		var updated int64
		if err := rows.Scan(&c.JID, &c.Phone, &c.Alias, &c.SystemName, &c.Name, &updated); err != nil {
			return nil, err
		}
		c.UpdatedAt = fromUnix(updated)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (d *DB) ListContacts(limit int) ([]Contact, error) {
	if limit <= 0 {
		limit = 100000
	}
	rows, err := d.q.ListContacts(storeCtx(), int64(limit))
	if err != nil {
		return nil, err
	}
	out := make([]Contact, 0, len(rows))
	for _, row := range rows {
		out = append(out, contactFromListRow(row))
	}
	return out, nil
}

func (d *DB) GetContact(jid string) (Contact, error) {
	row, err := d.q.GetContact(storeCtx(), jid)
	if err != nil {
		return Contact{}, err
	}
	c := contactFromGetRow(row)
	tags, _ := d.ListTags(jid)
	c.Tags = tags
	return c, nil
}

func (d *DB) SetSystemName(jid, systemName string) error {
	jid = strings.TrimSpace(jid)
	systemName = strings.TrimSpace(systemName)
	if jid == "" {
		return fmt.Errorf("jid is required")
	}
	if systemName == "" {
		return fmt.Errorf("system name is required")
	}
	now := nowUTC().Unix()
	n, err := d.q.SetSystemName(storeCtx(), storedb.SetSystemNameParams{
		SystemName: nullString(systemName),
		UpdatedAt:  now,
		Jid:        jid,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("contact not found: %s", jid)
	}
	return nil
}

func (d *DB) ClearAllSystemNames() (int64, error) {
	return d.q.ClearAllSystemNames(storeCtx(), nowUTC().Unix())
}

func (d *DB) CountSystemNames() (int64, error) {
	return d.q.CountSystemNames(storeCtx())
}

func (d *DB) ListTags(jid string) ([]string, error) {
	return d.q.ListTags(storeCtx(), jid)
}

func (d *DB) ListContactAliases() (map[string]string, error) {
	rows, err := d.sql.Query("SELECT jid, alias FROM contact_aliases")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	aliases := make(map[string]string)
	for rows.Next() {
		var jid, alias string
		if err := rows.Scan(&jid, &alias); err != nil {
			return nil, err
		}
		aliases[jid] = alias
	}
	return aliases, rows.Err()
}

func (d *DB) UpsertContact(jid, phone, pushName, fullName, firstName, businessName string) error {
	return d.q.UpsertContact(storeCtx(), storedb.UpsertContactParams{
		Jid:          jid,
		Phone:        nullString(phone),
		PushName:     nullString(pushName),
		FullName:     nullString(fullName),
		FirstName:    nullString(firstName),
		BusinessName: nullString(businessName),
		UpdatedAt:    nowUTC().Unix(),
	})
}

func (d *DB) SetAlias(jids []string, alias string) error {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return fmt.Errorf("alias is required")
	}
	return d.updateContactMetadata(jids, func(q *storedb.Queries, jid string) error {
		return q.SetAlias(storeCtx(), storedb.SetAliasParams{Jid: jid, Alias: alias, UpdatedAt: nowUTC().Unix()})
	})
}

func (d *DB) RemoveAlias(jids []string) error {
	return d.updateContactMetadata(jids, func(q *storedb.Queries, jid string) error {
		return q.RemoveAlias(storeCtx(), jid)
	})
}

func (d *DB) AddTag(jids []string, tag string) error {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return fmt.Errorf("tag is required")
	}
	return d.updateContactMetadata(jids, func(q *storedb.Queries, jid string) error {
		return q.AddTag(storeCtx(), storedb.AddTagParams{Jid: jid, Tag: tag, UpdatedAt: nowUTC().Unix()})
	})
}

func (d *DB) RemoveTag(jids []string, tag string) error {
	return d.updateContactMetadata(jids, func(q *storedb.Queries, jid string) error {
		return q.RemoveTag(storeCtx(), storedb.RemoveTagParams{Jid: jid, Tag: tag})
	})
}

func (d *DB) updateContactMetadata(jids []string, update func(*storedb.Queries, string) error) error {
	if len(jids) == 0 {
		return fmt.Errorf("at least one contact JID is required")
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := d.q.WithTx(tx)
	for _, jid := range jids {
		if err := update(q, jid); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func contactFromListRow(row storedb.ListContactsRow) Contact {
	return Contact{
		JID:        row.Jid,
		Phone:      row.Phone,
		Alias:      sqlString(row.Alias),
		SystemName: sqlString(row.SystemName),
		Name:       sqlString(row.Name),
		UpdatedAt:  fromUnix(row.UpdatedAt),
	}
}

func contactFromGetRow(row storedb.GetContactRow) Contact {
	return Contact{
		JID:        row.Jid,
		Phone:      row.Phone,
		Alias:      sqlString(row.Alias),
		SystemName: sqlString(row.SystemName),
		Name:       sqlString(row.Name),
		UpdatedAt:  fromUnix(row.UpdatedAt),
	}
}

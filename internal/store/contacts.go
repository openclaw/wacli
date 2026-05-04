package store

import (
	"fmt"
	"strings"
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
		       COALESCE(NULLIF(c.full_name,''), NULLIF(c.push_name,''), NULLIF(c.business_name,''), NULLIF(c.first_name,''), ''),
		       c.updated_at
		FROM contacts c
		LEFT JOIN contact_aliases a ON a.jid = c.jid
		WHERE LOWER(COALESCE(a.alias,'')) LIKE LOWER(?) ESCAPE '\' OR LOWER(COALESCE(c.full_name,'')) LIKE LOWER(?) ESCAPE '\' OR LOWER(COALESCE(c.push_name,'')) LIKE LOWER(?) ESCAPE '\' OR LOWER(COALESCE(c.first_name,'')) LIKE LOWER(?) ESCAPE '\' OR LOWER(COALESCE(c.business_name,'')) LIKE LOWER(?) ESCAPE '\' OR LOWER(COALESCE(c.phone,'')) LIKE LOWER(?) ESCAPE '\' OR LOWER(c.jid) LIKE LOWER(?) ESCAPE '\'
		ORDER BY COALESCE(NULLIF(a.alias,''), NULLIF(c.full_name,''), NULLIF(c.push_name,''), NULLIF(c.business_name,''), NULLIF(c.first_name,''), c.jid)
		LIMIT ?`
	needle := likeContains(query)
	rows, err := d.sql.Query(q, needle, needle, needle, needle, needle, needle, needle, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Contact
	for rows.Next() {
		var c Contact
		var updated int64
		if err := rows.Scan(&c.JID, &c.Phone, &c.Alias, &c.Name, &updated); err != nil {
			return nil, err
		}
		c.UpdatedAt = fromUnix(updated)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (d *DB) GetContact(jid string) (Contact, error) {
	row := d.sql.QueryRow(`
		SELECT c.jid,
		       COALESCE(c.phone,''),
		       COALESCE(NULLIF(a.alias,''), ''),
		       COALESCE(NULLIF(c.full_name,''), NULLIF(c.push_name,''), NULLIF(c.business_name,''), NULLIF(c.first_name,''), ''),
		       c.updated_at
		FROM contacts c
		LEFT JOIN contact_aliases a ON a.jid = c.jid
		WHERE c.jid = ?
	`, jid)
	var c Contact
	var updated int64
	if err := row.Scan(&c.JID, &c.Phone, &c.Alias, &c.Name, &updated); err != nil {
		return Contact{}, err
	}
	c.UpdatedAt = fromUnix(updated)
	tags, _ := d.ListTags(jid)
	c.Tags = tags
	return c, nil
}

func (d *DB) ListTags(jid string) ([]string, error) {
	rows, err := d.sql.Query(`SELECT tag FROM contact_tags WHERE jid = ? ORDER BY tag`, jid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tags []string
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			return nil, err
		}
		tags = append(tags, tag)
	}
	return tags, rows.Err()
}

func (d *DB) UpsertContact(jid, phone, pushName, fullName, firstName, businessName string) error {
	now := nowUTC().Unix()
	_, err := d.sql.Exec(`
		INSERT INTO contacts(jid, phone, push_name, full_name, first_name, business_name, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(jid) DO UPDATE SET
			phone=COALESCE(NULLIF(excluded.phone,''), contacts.phone),
			push_name=COALESCE(NULLIF(excluded.push_name,''), contacts.push_name),
			full_name=COALESCE(NULLIF(excluded.full_name,''), contacts.full_name),
			first_name=COALESCE(NULLIF(excluded.first_name,''), contacts.first_name),
			business_name=COALESCE(NULLIF(excluded.business_name,''), contacts.business_name),
			updated_at=excluded.updated_at
	`, jid, phone, pushName, fullName, firstName, businessName, now)
	return err
}

func (d *DB) SetAlias(jid, alias string) error {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return fmt.Errorf("alias is required")
	}
	now := nowUTC().Unix()
	_, err := d.sql.Exec(`
		INSERT INTO contact_aliases(jid, alias, notes, updated_at)
		VALUES (?, ?, NULL, ?)
		ON CONFLICT(jid) DO UPDATE SET alias=excluded.alias, updated_at=excluded.updated_at
	`, jid, alias, now)
	return err
}

func (d *DB) RemoveAlias(jid string) error {
	_, err := d.sql.Exec(`DELETE FROM contact_aliases WHERE jid = ?`, jid)
	return err
}

func (d *DB) AddTag(jid, tag string) error {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return fmt.Errorf("tag is required")
	}
	now := nowUTC().Unix()
	_, err := d.sql.Exec(`
		INSERT INTO contact_tags(jid, tag, updated_at) VALUES(?, ?, ?)
		ON CONFLICT(jid, tag) DO UPDATE SET updated_at=excluded.updated_at
	`, jid, tag, now)
	return err
}

func (d *DB) RemoveTag(jid, tag string) error {
	_, err := d.sql.Exec(`DELETE FROM contact_tags WHERE jid = ? AND tag = ?`, jid, tag)
	return err
}

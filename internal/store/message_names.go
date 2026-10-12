package store

import (
	"fmt"
	"strings"
)

// ResolveMessageName accepts only an exact locally stored name. A caller parses
// numbers/JIDs first so explicit identifiers always retain precedence.
func (d *DB) ResolveMessageName(name string, includeChats bool) (string, error) {
	name = strings.TrimSpace(name)
	query := `SELECT DISTINCT jid,name FROM (
 SELECT c.jid,COALESCE(NULLIF(a.alias,''),NULLIF(c.system_name,''),NULLIF(c.full_name,''),NULLIF(c.push_name,''),NULLIF(c.business_name,''),NULLIF(c.first_name,''),'') AS name
 FROM contacts c LEFT JOIN contact_aliases a ON a.jid=c.jid
 WHERE ? COLLATE NOCASE IN (a.alias,c.system_name,c.full_name,c.push_name,c.business_name,c.first_name)
 UNION SELECT jid,alias AS name FROM contact_aliases WHERE alias=? COLLATE NOCASE`
	args := []any{name, name}
	if includeChats {
		query += ` UNION SELECT jid,name FROM chats WHERE name=? COLLATE NOCASE`
		args = append(args, name)
	}
	query += `) ORDER BY jid LIMIT 11`
	rows, err := d.readQuery().Query(query, args...)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var jids, candidates []string
	for rows.Next() {
		var jid, n string
		if err = rows.Scan(&jid, &n); err != nil {
			return "", err
		}
		if len(jids) > 0 && jids[len(jids)-1] == jid {
			continue
		}
		jids = append(jids, jid)
		candidates = append(candidates, fmt.Sprintf("%s (%s)", n, jid))
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	if len(jids) == 0 {
		return "", fmt.Errorf("no local exact name match for %q; supply a number or JID", name)
	}
	if len(jids) != 1 {
		if len(candidates) > 10 {
			candidates = candidates[:10]
			candidates = append(candidates, "more matches omitted")
		}
		return "", fmt.Errorf("ambiguous local name %q: %s; supply an explicit JID", name, strings.Join(candidates, ", "))
	}
	return jids[0], nil
}

// MessageContextInChats includes the hit and visible neighbors across verified
// phone/LID equivalents, using timestamp and row ID for chronological ties.
func (d *DB) MessageContextInChats(chats []string, target Message, before, after int) ([]Message, error) {
	var beforeMessages, afterMessages []Message
	var err error
	if before > 0 {
		beforeMessages, err = d.ListMessages(ListMessagesParams{ChatJIDs: chats, Limit: before, key: &messagePageKey{ts: target.Timestamp.Unix(), row: target.rowID}})
		if err != nil {
			return nil, err
		}
	}
	if after > 0 {
		afterMessages, err = d.ListMessages(ListMessagesParams{ChatJIDs: chats, Limit: after, Asc: true, key: &messagePageKey{ts: target.Timestamp.Unix(), row: target.rowID}})
		if err != nil {
			return nil, err
		}
	}

	for i, j := 0, len(beforeMessages)-1; i < j; i, j = i+1, j-1 {
		beforeMessages[i], beforeMessages[j] = beforeMessages[j], beforeMessages[i]
	}
	out := append(beforeMessages, target)
	return append(out, afterMessages...), nil
}

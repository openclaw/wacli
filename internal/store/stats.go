package store

type StoreStats struct {
	Messages      int64
	Chats         int64
	Contacts      int64
	Groups        int64
	LastMessageTS int64
}

func (d *DB) Stats() (StoreStats, error) {
	var s StoreStats
	row := d.sql.QueryRow(`
		SELECT
			(SELECT COUNT(*) FROM messages),
			(SELECT COUNT(*) FROM chats),
			(SELECT COUNT(*) FROM contacts),
			(SELECT COUNT(*) FROM groups),
			COALESCE((SELECT MAX(ts) FROM messages), 0)
	`)
	err := row.Scan(&s.Messages, &s.Chats, &s.Contacts, &s.Groups, &s.LastMessageTS)
	return s, err
}

package store

import "database/sql"

// GetSetting reads a value from the settings table. Returns "" if not found.
func (d *DB) GetSetting(key string) (string, error) {
	var value string
	err := d.sql.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

// SetSetting writes a key-value pair to the settings table (upsert).
func (d *DB) SetSetting(key, value string) error {
	_, err := d.sql.Exec(
		`INSERT INTO settings(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value,
	)
	return err
}

// Convenience constants for well-known settings keys.
const SettingReadOnly = "readonly"

// IsReadOnly returns true if this device was authed with --readonly.
func (d *DB) IsReadOnly() bool {
	v, err := d.GetSetting(SettingReadOnly)
	if err != nil {
		return false
	}
	return v == "1"
}

// SetReadOnly persists the read-only flag.
func (d *DB) SetReadOnly(enabled bool) error {
	val := "0"
	if enabled {
		val = "1"
	}
	return d.SetSetting(SettingReadOnly, val)
}

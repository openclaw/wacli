package store

import (
	"fmt"
	"strings"
)

func (d *DB) MarkAppStateRecoveryRequired(collection string) error {
	collection = strings.TrimSpace(collection)
	if collection == "" {
		return fmt.Errorf("app state collection is required")
	}
	_, err := d.sql.Exec(`
		INSERT INTO app_state_recovery_required(collection, marked_at)
		VALUES(?, ?)
		ON CONFLICT(collection) DO UPDATE SET marked_at = excluded.marked_at
	`, collection, nowUTC().Unix())
	if err != nil {
		return fmt.Errorf("mark app state recovery required: %w", err)
	}
	return nil
}

func (d *DB) AppStateRecoveryRequired(collection string) (bool, error) {
	collection = strings.TrimSpace(collection)
	if collection == "" {
		return false, fmt.Errorf("app state collection is required")
	}
	var exists bool
	if err := d.sql.QueryRow(`
		SELECT EXISTS(
			SELECT 1 FROM app_state_recovery_required WHERE collection = ?
		)
	`, collection).Scan(&exists); err != nil {
		return false, fmt.Errorf("check app state recovery marker: %w", err)
	}
	return exists, nil
}

func (d *DB) ClearAppStateRecoveryRequired(collection string) error {
	collection = strings.TrimSpace(collection)
	if collection == "" {
		return fmt.Errorf("app state collection is required")
	}
	if _, err := d.sql.Exec(`DELETE FROM app_state_recovery_required WHERE collection = ?`, collection); err != nil {
		return fmt.Errorf("clear app state recovery marker: %w", err)
	}
	return nil
}

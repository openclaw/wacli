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
		VALUES(?, 1)
		ON CONFLICT(collection) DO UPDATE
		SET marked_at = app_state_recovery_required.marked_at + 1
	`, collection)
	if err != nil {
		return fmt.Errorf("mark app state recovery required: %w", err)
	}
	return nil
}

// BeginAppStateRecovery atomically creates a write-ahead marker or returns the
// generation of a marker left by an earlier failure.
func (d *DB) BeginAppStateRecovery(collection string) (generation int64, alreadyRequired bool, err error) {
	collection = strings.TrimSpace(collection)
	if collection == "" {
		return 0, false, fmt.Errorf("app state collection is required")
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return 0, false, fmt.Errorf("begin app state recovery marker: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.Exec(`
		INSERT OR IGNORE INTO app_state_recovery_required(collection, marked_at)
		VALUES(?, 1)
	`, collection)
	if err != nil {
		return 0, false, fmt.Errorf("begin app state recovery marker: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return 0, false, fmt.Errorf("check app state recovery marker insert: %w", err)
	}
	if err := tx.QueryRow(`
		SELECT marked_at FROM app_state_recovery_required WHERE collection = ?
	`, collection).Scan(&generation); err != nil {
		return 0, false, fmt.Errorf("load app state recovery marker generation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, false, fmt.Errorf("commit app state recovery marker: %w", err)
	}
	return generation, inserted == 0, nil
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

func (d *DB) ClearAppStateRecoveryGeneration(collection string, generation int64) (bool, error) {
	collection = strings.TrimSpace(collection)
	if collection == "" {
		return false, fmt.Errorf("app state collection is required")
	}
	result, err := d.sql.Exec(`
		DELETE FROM app_state_recovery_required
		WHERE collection = ? AND marked_at = ?
	`, collection, generation)
	if err != nil {
		return false, fmt.Errorf("clear app state recovery marker generation: %w", err)
	}
	cleared, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("check cleared app state recovery marker generation: %w", err)
	}
	return cleared == 1, nil
}

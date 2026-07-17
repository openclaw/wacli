package store

import (
	"fmt"
	"strings"
)

func (d *DB) MarkAppStateRecoveryRequired(collection string) error {
	_, err := d.MarkAppStateRecoveryGeneration(collection)
	return err
}

func (d *DB) MarkAppStateRecoveryGeneration(collection string) (int64, error) {
	generations, err := d.MarkAppStateRecoveryGenerations([]string{collection})
	if err != nil {
		return 0, err
	}
	return generations[0], nil
}

func (d *DB) MarkAppStateRecoveryGenerations(collections []string) ([]int64, error) {
	if len(collections) == 0 {
		return nil, nil
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin app state recovery intents: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	generations := make([]int64, 0, len(collections))
	for _, collection := range collections {
		collection = strings.TrimSpace(collection)
		if collection == "" {
			return nil, fmt.Errorf("app state collection is required")
		}
		var generation int64
		if err := tx.QueryRow(`
			INSERT INTO app_state_recovery_intents(collection)
			VALUES(?)
			RETURNING id
		`, collection).Scan(&generation); err != nil {
			return nil, fmt.Errorf("mark app state recovery required: %w", err)
		}
		generations = append(generations, generation)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit app state recovery intents: %w", err)
	}
	return generations, nil
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
	if err := tx.QueryRow(`
		INSERT INTO app_state_recovery_intents(collection)
		VALUES(?)
		RETURNING id
	`, collection).Scan(&generation); err != nil {
		return 0, false, fmt.Errorf("begin app state recovery intent: %w", err)
	}
	if err := tx.QueryRow(`
		SELECT EXISTS(
			SELECT 1 FROM app_state_recovery_intents
			WHERE collection = ? AND id <> ?
		)
	`, collection, generation).Scan(&alreadyRequired); err != nil {
		return 0, false, fmt.Errorf("check existing app state recovery intents: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, false, fmt.Errorf("commit app state recovery marker: %w", err)
	}
	return generation, alreadyRequired, nil
}

func (d *DB) AppStateRecoveryRequired(collection string) (bool, error) {
	collection = strings.TrimSpace(collection)
	if collection == "" {
		return false, fmt.Errorf("app state collection is required")
	}
	var exists bool
	if err := d.sql.QueryRow(`
		SELECT EXISTS(
			SELECT 1 FROM app_state_recovery_intents WHERE collection = ?
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
	if _, err := d.sql.Exec(`DELETE FROM app_state_recovery_intents WHERE collection = ?`, collection); err != nil {
		return fmt.Errorf("clear app state recovery marker: %w", err)
	}
	return nil
}

func (d *DB) ClearAppStateRecoveryGeneration(collection string, generation int64) (bool, error) {
	collection = strings.TrimSpace(collection)
	if collection == "" {
		return false, fmt.Errorf("app state collection is required")
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return false, fmt.Errorf("begin clearing app state recovery intent generation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`
		DELETE FROM app_state_recovery_intents
		WHERE collection = ? AND id <= ?
	`, collection, generation); err != nil {
		return false, fmt.Errorf("clear app state recovery intent generation: %w", err)
	}
	var remaining bool
	if err := tx.QueryRow(`
		SELECT EXISTS(
			SELECT 1 FROM app_state_recovery_intents WHERE collection = ?
		)
	`, collection).Scan(&remaining); err != nil {
		return false, fmt.Errorf("check newer app state recovery intents: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit cleared app state recovery intents: %w", err)
	}
	return !remaining, nil
}

func (d *DB) ClearAppStateRecoveryIntent(collection string, generation int64) error {
	collection = strings.TrimSpace(collection)
	if collection == "" {
		return fmt.Errorf("app state collection is required")
	}
	if _, err := d.sql.Exec(`
		DELETE FROM app_state_recovery_intents
		WHERE collection = ? AND id = ?
	`, collection, generation); err != nil {
		return fmt.Errorf("clear app state recovery intent: %w", err)
	}
	return nil
}

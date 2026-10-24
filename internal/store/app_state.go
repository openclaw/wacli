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
	if _, err := d.sql.Exec(`
		INSERT INTO app_state_recovery_intents(collection)
		SELECT ? WHERE NOT EXISTS (
			SELECT 1 FROM app_state_recovery_intents WHERE collection = ?
		)
	`, collection, collection); err != nil {
		return fmt.Errorf("mark app state recovery required: %w", err)
	}
	return nil
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

func (d *DB) AppStateRecoveryCollections() ([]string, error) {
	rows, err := d.sql.Query(`SELECT DISTINCT collection FROM app_state_recovery_intents ORDER BY collection`)
	if err != nil {
		return nil, fmt.Errorf("list app state recovery intents: %w", err)
	}
	defer rows.Close()
	var collections []string
	for rows.Next() {
		var collection string
		if err := rows.Scan(&collection); err != nil {
			return nil, err
		}
		collections = append(collections, collection)
	}
	return collections, rows.Err()
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

// SetAppStateFullSyncBroken durably records whether the latest full sync of
// the collection failed verification. The flag survives restarts so a
// known-broken collection is not sent through another destructive full sync
// (stock whatsmeow deletes the saved collection version before decoding a
// full sync) on the first write after the process restarts.
func (d *DB) SetAppStateFullSyncBroken(collection string, broken bool) error {
	collection = strings.TrimSpace(collection)
	if collection == "" {
		return fmt.Errorf("app state collection is required")
	}
	var err error
	if broken {
		_, err = d.sql.Exec(`
			INSERT INTO app_state_full_sync_broken(collection, marked_at)
			VALUES(?, ?)
			ON CONFLICT(collection) DO UPDATE SET marked_at = excluded.marked_at
		`, collection, nowUTC().Unix())
	} else {
		_, err = d.sql.Exec(`DELETE FROM app_state_full_sync_broken WHERE collection = ?`, collection)
	}
	if err != nil {
		return fmt.Errorf("record app state full sync verification for %s: %w", collection, err)
	}
	return nil
}

// AppStateFullSyncBrokenCollections lists the collections whose latest full
// sync is durably recorded as failing verification. Tolerates stores created
// before the table existed (read-only opens skip schema maintenance).
func (d *DB) AppStateFullSyncBrokenCollections() ([]string, error) {
	exists, err := d.tableExists("app_state_full_sync_broken")
	if err != nil || !exists {
		return nil, err
	}
	rows, err := d.sql.Query(`SELECT collection FROM app_state_full_sync_broken ORDER BY collection`)
	if err != nil {
		return nil, fmt.Errorf("list app state full sync verification flags: %w", err)
	}
	defer rows.Close()
	var collections []string
	for rows.Next() {
		var collection string
		if err := rows.Scan(&collection); err != nil {
			return nil, err
		}
		collections = append(collections, collection)
	}
	return collections, rows.Err()
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

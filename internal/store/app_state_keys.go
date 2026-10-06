package store

import (
	"context"
	"fmt"
)

func validateAppStateKeyScope(account string, id []byte) error {
	if account == "" || len(id) == 0 {
		return fmt.Errorf("account and app-state key ID are required")
	}
	return nil
}

func (d *DB) MarkAppStateKeyUnavailable(ctx context.Context, account string, id []byte) error {
	if err := validateAppStateKeyScope(account, id); err != nil {
		return err
	}
	_, err := d.sql.ExecContext(ctx, `INSERT INTO unavailable_app_state_keys(account_jid,key_id) VALUES (?,?) ON CONFLICT DO NOTHING`, account, id)
	return err
}

func (d *DB) ClearAppStateKeyUnavailable(ctx context.Context, account string, id []byte) error {
	if err := validateAppStateKeyScope(account, id); err != nil {
		return err
	}
	_, err := d.sql.ExecContext(ctx, `DELETE FROM unavailable_app_state_keys WHERE account_jid=? AND key_id=?`, account, id)
	return err
}

func (d *DB) IsAppStateKeyUnavailable(ctx context.Context, account string, id []byte) (bool, error) {
	if err := validateAppStateKeyScope(account, id); err != nil {
		return false, err
	}
	var found bool
	err := d.sql.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM unavailable_app_state_keys WHERE account_jid=? AND key_id=?)`, account, id).Scan(&found)
	return found, err
}

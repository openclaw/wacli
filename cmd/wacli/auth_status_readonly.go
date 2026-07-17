package main

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/openclaw/wacli/internal/sqliteutil"
	"go.mau.fi/whatsmeow/types"
)

func readOnlyAuthStatus(storeDir string) (bool, string, error) {
	sessionPath := filepath.Join(storeDir, "session.db")
	if _, err := os.Stat(sessionPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, "", nil
		}
		return false, "", err
	}
	if strings.ContainsAny(sessionPath, "?#") {
		return false, "", fmt.Errorf("session path must not contain '?' or '#'")
	}
	db, err := sql.Open("sqlite3", readOnlySessionSQLiteURI(sessionPath))
	if err != nil {
		return false, "", fmt.Errorf("open session db: %w", err)
	}
	defer db.Close()

	var jid string
	err = db.QueryRow("SELECT jid FROM whatsmeow_device LIMIT 1").Scan(&jid)
	switch {
	case err == nil:
		jid = strings.TrimSpace(jid)
		if jid == "" {
			return false, "", nil
		}
		parsed, err := types.ParseJID(jid)
		if err != nil {
			return false, "", fmt.Errorf("parse auth JID: %w", err)
		}
		return true, parsed.ToNonAD().String(), nil
	case errors.Is(err, sql.ErrNoRows):
		return false, "", nil
	default:
		return false, "", fmt.Errorf("read auth status: %w", err)
	}
}

func readOnlySessionSQLiteURI(path string) string {
	params := "mode=ro&_query_only=1&_busy_timeout=5000"
	if !sqliteSessionSidecarsExist(path) {
		params += "&immutable=1"
	}
	return sqliteutil.FileURI(path, params)
}

func sqliteSessionSidecarsExist(path string) bool {
	for _, suffix := range []string{"-journal", "-wal", "-shm"} {
		if _, err := os.Stat(path + suffix); err == nil {
			return true
		}
	}
	return false
}

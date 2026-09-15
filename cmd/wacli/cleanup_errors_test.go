package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestBulkCleanupReportsDeletionFailures(t *testing.T) {
	for _, command := range [][]string{{"store", "cleanup"}, {"chats", "cleanup"}, {"groups", "prune"}} {
		for _, allFail := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/all_fail=%t", command[0], allFail), func(t *testing.T) {
				storeDir := seedPruneStore(t)
				raw, err := sql.Open("sqlite3", filepath.Join(storeDir, "wacli.db"))
				if err != nil {
					t.Fatal(err)
				}
				condition := "WHEN OLD.jid = 'old-left@g.us'"
				if allFail {
					condition = ""
				}
				_, err = raw.Exec(`CREATE TRIGGER reject_cleanup BEFORE DELETE ON chats ` + condition + ` BEGIN SELECT RAISE(FAIL, 'synthetic deletion failure'); END`)
				if closeErr := raw.Close(); closeErr != nil {
					t.Fatal(closeErr)
				}
				if err != nil {
					t.Fatal(err)
				}

				args := append([]string{"--store", storeDir, "--json"}, command...)
				args = append(args, "--days", "10", "--confirm")
				var commandErr error
				var stdout string
				stderr := captureRootStderr(t, func() {
					stdout = captureRootStdout(t, func() { commandErr = execute(args) })
				})
				if commandErr == nil || !strings.Contains(commandErr.Error(), "synthetic deletion failure") {
					t.Errorf("error = %v, want underlying deletion failure", commandErr)
				}
				if stdout != "" {
					t.Errorf("failed cleanup emitted success output: %s", stdout)
				}
				var result struct {
					Success bool   `json:"success"`
					Error   string `json:"error"`
				}
				if err := json.Unmarshal([]byte(stderr), &result); err != nil || result.Success || !strings.Contains(result.Error, "synthetic deletion failure") {
					t.Errorf("stderr is not a JSON failure: %s (%v)", stderr, err)
				}
				deleted := 2
				if command[0] == "groups" {
					deleted = 1
				}
				if allFail {
					deleted = 0
				}
				if !strings.Contains(result.Error, fmt.Sprintf("deleted %d ", deleted)) {
					t.Errorf("failure lacks the completed deletion count: %s", result.Error)
				}
				db := openPruneStore(t, storeDir)
				defer db.Close()
				if _, err := db.GetChat("old-left@g.us"); err != nil {
					t.Errorf("failed chat deletion must roll back: %v", err)
				}
				_, err = db.GetChat("recent-left@g.us")
				if allFail && err != nil {
					t.Errorf("failed chat was removed: %v", err)
				} else if !allFail && err == nil {
					t.Error("other eligible chats should still be deleted")
				}
			})
		}
	}
}

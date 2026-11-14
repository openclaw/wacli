package store

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func messageContactsSchema(t *testing.T, db *DB) string {
	t.Helper()
	rows, err := db.sql.Query(`SELECT type, name, COALESCE(sql, '') FROM sqlite_master WHERE tbl_name = 'message_contacts' ORDER BY type, name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var kind, name, stmt string
		if err := rows.Scan(&kind, &name, &stmt); err != nil {
			t.Fatal(err)
		}
		out = append(out, kind+" "+name+" "+strings.Join(strings.Fields(stmt), " "))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(out, "\n")
}

func TestMessageContactsMigrationIsVersionedAndIdempotent(t *testing.T) {
	fresh, err := Open(filepath.Join(t.TempDir(), "wacli.db"))
	if err != nil {
		t.Fatal(err)
	}
	want := messageContactsSchema(t, fresh)
	if !strings.Contains(want, "table message_contacts") || !strings.Contains(want, "trigger retrieval_message_contacts_insert") {
		t.Fatalf("fresh store lacks message_contacts shape:\n%s", want)
	}
	var recorded int
	if err := fresh.sql.QueryRow(`SELECT count(*) FROM schema_migrations WHERE version = 40 AND name = 'message contact cards'`).Scan(&recorded); err != nil || recorded != 1 {
		t.Fatalf("fresh store did not record migration 40: %d, %v", recorded, err)
	}
	if err := fresh.Close(); err != nil {
		t.Fatal(err)
	}

	// Fork builds recorded 34..38 for unrelated schema; this migration must
	// still run, and must tolerate a shape that is already present.
	for _, shapePresent := range []bool{true, false} {
		t.Run(fmt.Sprint("shapePresent=", shapePresent), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "wacli.db")
			db, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			statements := []string{`DELETE FROM schema_migrations WHERE version = 40`}
			for v := 34; v <= 38; v++ {
				statements = append(statements, fmt.Sprintf(`INSERT OR IGNORE INTO schema_migrations(version, name, applied_at) VALUES(%d, 'fork %d', 1)`, v, v))
			}
			if !shapePresent {
				statements = append(statements, `DROP TABLE message_contacts`)
			}
			for _, statement := range statements {
				if _, err := db.sql.Exec(statement); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = Open(path)
			if err != nil {
				t.Fatalf("Open recorded-through-38 store: %v", err)
			}
			defer db.Close()
			if got := messageContactsSchema(t, db); got != want {
				t.Fatalf("message_contacts schema differs from a fresh store:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

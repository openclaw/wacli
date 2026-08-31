package main

import (
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
)

func TestGroupsListWarnsOnlyWhenMatchingResultsAreTruncated(t *testing.T) {
	storeDir := t.TempDir()
	db, err := store.Open(filepath.Join(storeDir, "wacli.db"))
	if err != nil {
		t.Fatal(err)
	}
	for i := range 51 {
		if err := db.UpsertGroup(fmt.Sprintf("%d@g.us", i), "Matching group", "", time.Unix(int64(i+1), 0)); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.UpsertGroup("left@g.us", "Matching group", "", time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkGroupLeft("left@g.us", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		args   []string
		rows   int
		warn   bool
		events bool
	}{
		{name: "default", rows: 50, warn: true},
		{name: "zero uses default", args: []string{"--limit", "0"}, rows: 50, warn: true},
		{name: "negative uses default", args: []string{"--limit", "-1"}, rows: 50, warn: true},
		{name: "matching overflow", args: []string{"--query", "Matching", "--limit", "1"}, rows: 1, warn: true},
		{name: "exact limit ignores left groups", args: []string{"--limit", "51"}, rows: 51},
		{name: "below limit", args: []string{"--limit", "52"}, rows: 51},
		{name: "no matches", args: []string{"--query", "absent"}, rows: 0},
		{name: "maximum limit", args: []string{"--limit", strconv.Itoa(math.MaxInt)}, rows: 51},
		{name: "events", args: []string{"--limit", "1"}, rows: 1, warn: true, events: true},
	} {
		for _, asJSON := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/json=%t", tc.name, asJSON), func(t *testing.T) {
				cmd := newGroupsListCmd(&rootFlags{storeDir: storeDir, readOnly: true, asJSON: asJSON, events: tc.events})
				cmd.SetArgs(tc.args)
				var stdout string
				stderr := captureRootStderr(t, func() {
					stdout = captureRootStdout(t, func() {
						if err := cmd.Execute(); err != nil {
							t.Fatal(err)
						}
					})
				})
				if asJSON {
					var result struct {
						Success bool
						Data    []store.Group
					}
					if err := json.Unmarshal([]byte(stdout), &result); err != nil {
						t.Fatal(err)
					}
					if !result.Success || len(result.Data) != tc.rows {
						t.Fatalf("JSON result = %+v, want %d rows", result, tc.rows)
					}
				} else if lines := strings.Count(stdout, "\n"); lines != tc.rows+1 {
					t.Fatalf("table lines = %d, want %d", lines, tc.rows+1)
				}
				if !tc.warn {
					if stderr != "" {
						t.Fatalf("unexpected warning: %q", stderr)
					}
					return
				}
				want := fmt.Sprintf("showing first %d matching groups; more are available; increase --limit", tc.rows)
				if !strings.Contains(stderr, want) {
					t.Fatalf("missing truncation warning %q in %q", want, stderr)
				}
				if tc.events {
					var event struct{ Event string }
					if err := json.Unmarshal([]byte(stderr), &event); err != nil {
						t.Fatalf("stderr is not one JSON event: %v", err)
					}
					if event.Event != "warning" {
						t.Fatalf("event = %q, want warning", event.Event)
					}
				}
			})
		}
	}
}

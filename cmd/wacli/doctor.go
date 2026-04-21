package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steipete/wacli/internal/config"
	"github.com/steipete/wacli/internal/lock"
	"github.com/steipete/wacli/internal/out"
	"github.com/steipete/wacli/internal/store"
)

func parseLockOwnerPID(lockInfo string) int {
	for _, line := range strings.Split(lockInfo, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "pid=") {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "pid=")))
		if err == nil && pid > 0 {
			return pid
		}
	}
	return 0
}

func doctorConnectionState(authed, connected, lockHeld, connect bool) string {
	switch {
	case connected:
		return "connected"
	case authed && lockHeld && !connect:
		return "locked_by_other_process"
	default:
		return "disconnected"
	}
}

type doctorStoreStats struct {
	Messages   int64  `json:"messages"`
	Chats      int64  `json:"chats"`
	Contacts   int64  `json:"contacts"`
	Groups     int64  `json:"groups"`
	LastSyncAt string `json:"last_sync_at,omitempty"`
}

type doctorReport struct {
	StoreDir        string            `json:"store_dir"`
	LockHeld        bool              `json:"lock_held"`
	LockInfo        string            `json:"lock_info,omitempty"`
	LockOwnerPID    int               `json:"lock_owner_pid,omitempty"`
	Authed          bool              `json:"authenticated"`
	LinkedJID       string            `json:"linked_jid,omitempty"`
	Connected       bool              `json:"connected"`
	ConnectionState string            `json:"connection_state"`
	FTSEnabled      bool              `json:"fts_enabled"`
	Store           *doctorStoreStats `json:"store,omitempty"`
	StoreError      string            `json:"store_error,omitempty"`
}

func doctorStoreStatsFromStoreStats(stats store.StoreStats) doctorStoreStats {
	out := doctorStoreStats{
		Messages: stats.Messages,
		Chats:    stats.Chats,
		Contacts: stats.Contacts,
		Groups:   stats.Groups,
	}
	if stats.LastMessageTS > 0 {
		out.LastSyncAt = time.Unix(stats.LastMessageTS, 0).UTC().Format(time.RFC3339)
	}
	return out
}

func writeDoctorReport(w io.Writer, rep doctorReport) {
	tw := newTableWriter(w)
	fmt.Fprintf(tw, "STORE\t%s\n", rep.StoreDir)
	fmt.Fprintf(tw, "LOCKED\t%v\n", rep.LockHeld)
	if rep.LockHeld && rep.LockInfo != "" {
		fmt.Fprintf(tw, "LOCK_INFO\t%s\n", rep.LockInfo)
	}
	if rep.LockOwnerPID > 0 {
		fmt.Fprintf(tw, "LOCK_OWNER_PID\t%d\n", rep.LockOwnerPID)
	}
	fmt.Fprintf(tw, "AUTHENTICATED\t%v\n", rep.Authed)
	if rep.LinkedJID != "" {
		fmt.Fprintf(tw, "LINKED_JID\t%s\n", rep.LinkedJID)
	}
	fmt.Fprintf(tw, "CONNECTED\t%v\n", rep.Connected)
	fmt.Fprintf(tw, "CONNECTION_STATE\t%s\n", rep.ConnectionState)
	fmt.Fprintf(tw, "FTS5\t%v\n", rep.FTSEnabled)
	if rep.Store != nil {
		fmt.Fprintf(tw, "MESSAGES\t%d\n", rep.Store.Messages)
		fmt.Fprintf(tw, "CHATS\t%d\n", rep.Store.Chats)
		fmt.Fprintf(tw, "CONTACTS\t%d\n", rep.Store.Contacts)
		fmt.Fprintf(tw, "GROUPS\t%d\n", rep.Store.Groups)
		if rep.Store.LastSyncAt != "" {
			fmt.Fprintf(tw, "LAST_SYNC\t%s\n", rep.Store.LastSyncAt)
		}
	}
	_ = tw.Flush()
}

func newDoctorCmd(flags *rootFlags) *cobra.Command {
	var connect bool

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnostics for store/auth/search",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			storeDir := flags.storeDir
			if storeDir == "" {
				storeDir = config.DefaultStoreDir()
			}
			storeDir, _ = filepath.Abs(storeDir)

			var lockHeld bool
			var lockInfo string
			if b, err := os.ReadFile(filepath.Join(storeDir, "LOCK")); err == nil {
				lockInfo = strings.TrimSpace(string(b))
			}
			if lk, err := lock.Acquire(storeDir); err == nil {
				_ = lk.Release()
			} else {
				lockHeld = true
			}

			var storeErr string
			a, lk, err := newApp(ctx, flags, connect, true)
			if err != nil {
				storeErr = err.Error()
			} else {
				defer closeApp(a, lk)
			}

			var authed bool
			var connected bool
			var linkedJID string
			if a != nil {
				if err := a.OpenWA(); err == nil {
					authed = a.WA().IsAuthed()
					if authed {
						linkedJID = a.WA().LinkedJID()
					}
				}
				if connect && authed {
					if err := a.Connect(ctx, false, nil); err == nil {
						connected = true
					}
				}
			}
			lockOwnerPID := parseLockOwnerPID(lockInfo)

			var stats *doctorStoreStats
			if a != nil {
				if raw, err := a.DB().Stats(); err == nil {
					converted := doctorStoreStatsFromStoreStats(raw)
					stats = &converted
				}
			}

			rep := doctorReport{
				StoreDir:        storeDir,
				LockHeld:        lockHeld,
				LockInfo:        lockInfo,
				LockOwnerPID:    lockOwnerPID,
				Authed:          authed,
				LinkedJID:       linkedJID,
				Connected:       connected,
				ConnectionState: doctorConnectionState(authed, connected, lockHeld, connect),
				FTSEnabled:      a != nil && a.DB().HasFTS(),
				Store:           stats,
				StoreError:      storeErr,
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, rep)
			}

			writeDoctorReport(os.Stdout, rep)

			if rep.StoreError != "" {
				fmt.Fprintf(os.Stdout, "\nERROR: store could not be opened: %s\n", rep.StoreError)
				fmt.Fprintln(os.Stdout, "Tip: check that the store directory exists and is not corrupted.")
			}
			if rep.LockHeld {
				fmt.Fprintln(os.Stdout, "\nTip: stop the running `wacli sync` before running write operations.")
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&connect, "connect", false, "try connecting to WhatsApp (requires store lock)")
	return cmd
}

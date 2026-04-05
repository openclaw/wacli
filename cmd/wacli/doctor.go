package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/steipete/wacli/internal/config"
	"github.com/steipete/wacli/internal/lock"
	"github.com/steipete/wacli/internal/out"
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

			a, lk, err := newApp(ctx, flags, connect, true)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			var authed bool
			var connected bool
			if err := a.OpenWA(); err == nil {
				authed = a.WA().IsAuthed()
			}
			if connect && authed {
				if err := a.Connect(ctx, false, nil); err == nil {
					connected = true
				}
			}

			connectionState := "disconnected"
			lockOwnerPID := parseLockOwnerPID(lockInfo)
			if connected {
				connectionState = "connected"
			} else if authed && lockHeld && !connect {
				connectionState = "locked_by_other_process"
			}

			type report struct {
				StoreDir        string `json:"store_dir"`
				LockHeld        bool   `json:"lock_held"`
				LockInfo        string `json:"lock_info,omitempty"`
				LockOwnerPID    int    `json:"lock_owner_pid,omitempty"`
				Authed          bool   `json:"authenticated"`
				Connected       bool   `json:"connected"`
				ConnectionState string `json:"connection_state"`
				FTSEnabled      bool   `json:"fts_enabled"`
			}

			rep := report{
				StoreDir:        storeDir,
				LockHeld:        lockHeld,
				LockInfo:        lockInfo,
				LockOwnerPID:    lockOwnerPID,
				Authed:          authed,
				Connected:       connected,
				ConnectionState: connectionState,
				FTSEnabled:      a.DB().HasFTS(),
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, rep)
			}

			w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
			fmt.Fprintf(w, "STORE\t%s\n", rep.StoreDir)
			fmt.Fprintf(w, "LOCKED\t%v\n", rep.LockHeld)
			if rep.LockHeld && rep.LockInfo != "" {
				fmt.Fprintf(w, "LOCK_INFO\t%s\n", rep.LockInfo)
			}
			if rep.LockOwnerPID > 0 {
				fmt.Fprintf(w, "LOCK_OWNER_PID\t%d\n", rep.LockOwnerPID)
			}
			fmt.Fprintf(w, "AUTHENTICATED\t%v\n", rep.Authed)
			fmt.Fprintf(w, "CONNECTED\t%v\n", rep.Connected)
			fmt.Fprintf(w, "CONNECTION_STATE\t%s\n", rep.ConnectionState)
			fmt.Fprintf(w, "FTS5\t%v\n", rep.FTSEnabled)
			_ = w.Flush()

			if rep.LockHeld {
				fmt.Fprintln(os.Stdout, "\nTip: stop the running `wacli sync` before running write operations.")
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&connect, "connect", false, "try connecting to WhatsApp (requires store lock)")
	return cmd
}

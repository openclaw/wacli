package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/steipete/wacli/internal/lock"
	"github.com/steipete/wacli/internal/out"
)

func newDoctorCmd(flags *rootFlags) *cobra.Command {
	var connect bool

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnostics for store/auth/search",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			_, accountDir, accountName, err := resolveStoreDir(flags)
			if err != nil {
				return err
			}

			var lockHeld bool
			var lockInfo string
			if b, err := os.ReadFile(filepath.Join(accountDir, "LOCK")); err == nil {
				lockInfo = strings.TrimSpace(string(b))
			}
			if lk, err := lock.Acquire(accountDir); err == nil {
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

			type report struct {
				Account    string `json:"account"`
				StoreDir   string `json:"store_dir"`
				LockHeld   bool   `json:"lock_held"`
				LockInfo   string `json:"lock_info,omitempty"`
				Authed     bool   `json:"authenticated"`
				Connected  bool   `json:"connected"`
				FTSEnabled bool   `json:"fts_enabled"`
			}

			rep := report{
				Account:    accountName,
				StoreDir:   accountDir,
				LockHeld:   lockHeld,
				LockInfo:   lockInfo,
				Authed:     authed,
				Connected:  connected,
				FTSEnabled: a.DB().HasFTS(),
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, rep)
			}

			w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
			fmt.Fprintf(w, "ACCOUNT\t%s\n", rep.Account)
			fmt.Fprintf(w, "STORE\t%s\n", rep.StoreDir)
			fmt.Fprintf(w, "LOCKED\t%v\n", rep.LockHeld)
			if rep.LockHeld && rep.LockInfo != "" {
				fmt.Fprintf(w, "LOCK_INFO\t%s\n", rep.LockInfo)
			}
			fmt.Fprintf(w, "AUTHENTICATED\t%v\n", rep.Authed)
			fmt.Fprintf(w, "CONNECTED\t%v\n", rep.Connected)
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

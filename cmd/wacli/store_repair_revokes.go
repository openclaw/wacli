package main

import (
	"context"
	"fmt"
	"os"

	"github.com/openclaw/wacli/internal/out"
	"github.com/spf13/cobra"
)

func newStoreRepairRevokesCmd(flags *rootFlags) *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "repair-revokes",
		Short: "Move misfiled incoming revokes onto the original messages",
		Long: `Older versions filed a revoke sent by the other party as a new row in the
owner's own chat instead of marking the original message as deleted. This finds
those rows, marks each original revoked (keeping its text and media) and removes
the misfiled row, in one transaction. Local store only; nothing is sent to
WhatsApp. Dry-run by default; pass --dry-run=false to apply.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !dryRun {
				if err := flags.requireWritable(); err != nil {
					return err
				}
			}
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()
			a, lk, err := newApp(ctx, flags, true, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			found, err := a.DB().FindMisfiledRevokes()
			if err != nil {
				return err
			}
			repaired := 0
			if !dryRun {
				if repaired, err = a.DB().RepairMisfiledRevokes(); err != nil {
					return err
				}
			}
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{"dry_run": dryRun, "found": len(found), "repaired": repaired, "rows": found})
			}
			for _, m := range found {
				fmt.Fprintf(os.Stdout, "%s -> %s  %s\n", m.MisfiledChatJID, m.TargetChatJID, sanitize(m.MsgID))
			}
			if dryRun {
				fmt.Fprintf(os.Stderr, "Would repair %d misfiled revoke(s). Re-run with --dry-run=false to apply.\n", len(found))
			} else {
				fmt.Fprintf(os.Stderr, "Repaired %d misfiled revoke(s).\n", repaired)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", true, "only print what would change (use --dry-run=false to apply)")
	return cmd
}

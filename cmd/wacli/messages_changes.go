package main

import (
	"context"
	"fmt"
	"os"

	"github.com/openclaw/wacli/internal/out"
	"github.com/spf13/cobra"
)

func newMessagesChangesCmd(flags *rootFlags) *cobra.Command {
	var since string
	var checkpoint bool
	var limit int
	cmd := &cobra.Command{Use: "changes", Short: "Read content-free local changes or obtain a checkpoint", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := withTimeout(context.Background(), flags)
		defer cancel()
		a, lk, err := newApp(ctx, flags, false, false)
		if err != nil {
			return err
		}
		defer closeApp(a, lk)
		page, err := a.DB().MessageChanges(since, checkpoint, limit)
		if err != nil {
			return err
		}
		if flags.asJSON {
			return out.WriteJSON(os.Stdout, page)
		}
		tw := newTableWriter(os.Stdout)
		fmt.Fprintln(tw, "SEQUENCE\tOPERATION\tCHAT\tMESSAGE\tREASON")
		for _, c := range page.Changes {
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n", c.Sequence, sanitize(c.Operation), sanitize(c.ChatJID), sanitize(c.MsgID), sanitize(c.Reason))
		}
		if err = tw.Flush(); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Checkpoint: %s (more: %v)\n", page.NextCheckpoint, page.HasMore)
		return nil
	}}
	cmd.Flags().StringVar(&since, "since", "", "checkpoint token or beginning (expires after retention)")
	cmd.Flags().BoolVar(&checkpoint, "checkpoint", false, "capture current position without historical entries")
	cmd.Flags().IntVar(&limit, "limit", 50, "change page size (maximum 100000)")
	return cmd
}

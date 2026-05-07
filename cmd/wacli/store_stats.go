package main

import (
	"context"
	"fmt"
	"os"

	"github.com/openclaw/wacli/internal/out"
	"github.com/spf13/cobra"
)

func newStoreStatsCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Show store statistics",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, false, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			_ = ctx

			chats, err := a.DB().ListChats("", 0)
			if err != nil {
				return err
			}

			groups, err := a.DB().ListGroups("", 0)
			if err != nil {
				return err
			}

			leftGroups, err := a.DB().ListLeftGroups()
			if err != nil {
				return err
			}

			totalMessages, err := a.DB().CountMessages()
			if err != nil {
				return err
			}

			stats := map[string]any{
				"chats":       len(chats),
				"groups":      len(groups),
				"left_groups": len(leftGroups),
				"messages":    totalMessages,
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, stats)
			}

			fmt.Fprintf(os.Stdout, "Store Statistics:\n")
			fmt.Fprintf(os.Stdout, "  Chats:       %d\n", len(chats))
			fmt.Fprintf(os.Stdout, "  Groups:      %d\n", len(groups))
			fmt.Fprintf(os.Stdout, "  Left Groups: %d\n", len(leftGroups))
			fmt.Fprintf(os.Stdout, "  Messages:    %d\n", totalMessages)
			return nil
		},
	}
	return cmd
}

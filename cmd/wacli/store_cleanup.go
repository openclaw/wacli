package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/openclaw/wacli/internal/out"
	"github.com/spf13/cobra"
)

func newStoreCleanupCmd(flags *rootFlags) *cobra.Command {
	var days int
	var dryRun bool
	var confirm bool
	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Clean up old data from local store",
		Long: `Clean up old messages and chats from local storage.

Removes chats with no recent activity and their associated messages.
Use --days to set the threshold (default: 365 days).
Use --dry-run to preview what would be deleted.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := flags.requireWritable(); err != nil {
				return err
			}

			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, true, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			_ = ctx

			chats, err := a.DB().ListChatsOlderThan(days)
			if err != nil {
				return err
			}

			if len(chats) == 0 {
				if flags.asJSON {
					return out.WriteJSON(os.Stdout, map[string]any{"deleted": 0, "message": "nothing to clean up"})
				}
				fmt.Fprintln(os.Stderr, "Nothing to clean up.")
				return nil
			}

			var totalMessages int64
			for _, c := range chats {
				count, _ := a.DB().CountChatMessages(c.JID)
				totalMessages += count
			}

			if dryRun {
				if flags.asJSON {
					return out.WriteJSON(os.Stdout, map[string]any{
						"would_delete_chats":    len(chats),
						"would_delete_messages": totalMessages,
						"days":                  days,
					})
				}
				fmt.Fprintf(os.Stderr, "Would delete %d chat(s) with %d total message(s) (older than %d days):\n", len(chats), totalMessages, days)
				for _, c := range chats {
					name := c.Name
					if name == "" {
						name = c.JID
					}
					count, _ := a.DB().CountChatMessages(c.JID)
					fmt.Fprintf(os.Stderr, "  - %s (%s, %d messages)\n", name, c.JID, count)
				}
				fmt.Fprintln(os.Stderr, "\nRun without --dry-run to actually delete.")
				return nil
			}

			if !confirm {
				fmt.Fprintf(os.Stderr, "About to delete %d chat(s) with %d total message(s). This cannot be undone.\n", len(chats), totalMessages)
				fmt.Fprint(os.Stderr, "Continue? [y/N] ")
				reader := bufio.NewReader(os.Stdin)
				answer, _ := reader.ReadString('\n')
				answer = strings.TrimSpace(strings.ToLower(answer))
				if answer != "y" && answer != "yes" {
					fmt.Fprintln(os.Stderr, "Aborted.")
					return nil
				}
			}

			var deletedChats, deletedMessages int64
			for _, c := range chats {
				count, _ := a.DB().CountChatMessages(c.JID)
				if err := a.DB().DeleteChat(c.JID); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to delete chat %s: %v\n", c.JID, err)
					continue
				}
				deletedChats++
				deletedMessages += count
				if !flags.asJSON {
					name := c.Name
					if name == "" {
						name = c.JID
					}
					fmt.Fprintf(os.Stderr, "Deleted %s (%d messages)\n", name, count)
				}
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{
					"deleted_chats":    deletedChats,
					"deleted_messages": deletedMessages,
				})
			}
			fmt.Fprintf(os.Stderr, "\nDone. Deleted %d chat(s) with %d message(s).\n", deletedChats, deletedMessages)
			return nil
		},
	}
	cmd.Flags().IntVar(&days, "days", 365, "delete data older than N days")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be deleted without deleting")
	cmd.Flags().BoolVar(&confirm, "confirm", false, "skip confirmation prompt")
	return cmd
}

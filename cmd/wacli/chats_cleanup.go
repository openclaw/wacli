package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/out"
	"github.com/spf13/cobra"
)

func newChatsCleanupCmd(flags *rootFlags) *cobra.Command {
	var days int
	var jid string
	var dryRun bool
	var confirm bool
	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Clean up old chats from local storage",
		Long: `Clean up chats that have no recent activity.

By default, removes chats with no messages in the last 365 days.
Use --days to adjust the threshold. Use --dry-run to preview what would be deleted.`,
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

			if jid != "" {
				return cleanupSingleChat(ctx, a, jid, dryRun, confirm, flags.asJSON)
			}

			chats, err := a.DB().ListChatsOlderThan(days)
			if err != nil {
				return err
			}

			if len(chats) == 0 {
				if flags.asJSON {
					return out.WriteJSON(os.Stdout, map[string]any{"deleted": 0, "message": "no chats to clean up"})
				}
				fmt.Fprintln(os.Stderr, "No chats to clean up.")
				return nil
			}

			if dryRun {
				if flags.asJSON {
					return out.WriteJSON(os.Stdout, map[string]any{"would_delete": len(chats), "chats": chats})
				}
				fmt.Fprintf(os.Stderr, "Would delete %d chat(s):\n", len(chats))
				for _, c := range chats {
					name := c.Name
					if name == "" {
						name = c.JID
					}
					fmt.Fprintf(os.Stderr, "  - %s (%s)\n", name, c.JID)
				}
				fmt.Fprintln(os.Stderr, "\nRun without --dry-run to actually delete.")
				return nil
			}

			if !confirm {
				fmt.Fprintf(os.Stderr, "About to delete %d chat(s). This cannot be undone.\n", len(chats))
				fmt.Fprint(os.Stderr, "Continue? [y/N] ")
				reader := bufio.NewReader(os.Stdin)
				answer, _ := reader.ReadString('\n')
				answer = strings.TrimSpace(strings.ToLower(answer))
				if answer != "y" && answer != "yes" {
					fmt.Fprintln(os.Stderr, "Aborted.")
					return nil
				}
			}

			var deleted int
			for _, c := range chats {
				msgCount, _ := a.DB().CountChatMessages(c.JID)
				if err := a.DB().DeleteChat(c.JID); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to delete chat %s: %v\n", c.JID, err)
					continue
				}
				deleted++
				if !flags.asJSON {
					name := c.Name
					if name == "" {
						name = c.JID
					}
					fmt.Fprintf(os.Stderr, "Deleted %s (%d messages)\n", name, msgCount)
				}
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{"deleted": deleted})
			}
			fmt.Fprintf(os.Stderr, "\nDone. Deleted %d chat(s).\n", deleted)
			return nil
		},
	}
	cmd.Flags().IntVar(&days, "days", 365, "delete chats with no messages in the last N days")
	cmd.Flags().StringVar(&jid, "jid", "", "delete a specific chat by JID")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be deleted without deleting")
	cmd.Flags().BoolVar(&confirm, "confirm", false, "skip confirmation prompt")
	return cmd
}

func cleanupSingleChat(ctx context.Context, a *app.App, jid string, dryRun, confirm, asJSON bool) error {
	chat, err := a.DB().GetChat(jid)
	if err != nil {
		return fmt.Errorf("chat not found: %s", jid)
	}

	msgCount, _ := a.DB().CountChatMessages(jid)

	if dryRun {
		if asJSON {
			return out.WriteJSON(os.Stdout, map[string]any{
				"would_delete":  1,
				"chat":          chat,
				"message_count": msgCount,
			})
		}
		name := chat.Name
		if name == "" {
			name = chat.JID
		}
		fmt.Fprintf(os.Stderr, "Would delete chat: %s (%s, %d messages)\n", name, chat.JID, msgCount)
		return nil
	}

	if !confirm {
		name := chat.Name
		if name == "" {
			name = chat.JID
		}
		fmt.Fprintf(os.Stderr, "About to delete chat: %s (%s, %d messages). This cannot be undone.\n", name, chat.JID, msgCount)
		fmt.Fprint(os.Stderr, "Continue? [y/N] ")
		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		answer = strings.TrimSpace(strings.ToLower(answer))
		if answer != "y" && answer != "yes" {
			fmt.Fprintln(os.Stderr, "Aborted.")
			return nil
		}
	}

	if err := a.DB().DeleteChat(jid); err != nil {
		return err
	}

	if asJSON {
		return out.WriteJSON(os.Stdout, map[string]any{"deleted": 1, "jid": jid, "messages_deleted": msgCount})
	}
	fmt.Fprintf(os.Stderr, "Deleted chat %s (%d messages)\n", jid, msgCount)
	return nil
}

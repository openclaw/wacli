package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/store"
	"github.com/spf13/cobra"
)

func newMessagesPurgeCmd(flags *rootFlags) *cobra.Command {
	var chat string
	var id string
	var dryRun bool
	var confirm bool
	cmd := &cobra.Command{
		Use:   "purge",
		Short: "Permanently remove one tombstoned message payload",
		Long: `Permanently erase the retained payload of one tombstoned message.

This only purges the retained local wacli payload. It does not delete anything
from WhatsApp. A minimal suppression tombstone remains so later imports cannot
restore the payload. Live messages must first receive an explicit deletion event.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			chat = strings.TrimSpace(chat)
			id = strings.TrimSpace(id)
			if chat == "" || id == "" {
				return fmt.Errorf("--chat and --id are required")
			}
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

			msg, err := a.DB().GetMessage(chat, id)
			if err != nil {
				return err
			}
			if msg.DeletedAt == nil {
				return store.ErrMessageNotTombstoned
			}
			if dryRun {
				if flags.asJSON {
					return out.WriteJSON(os.Stdout, map[string]any{"would_purge": 1, "message": msg})
				}
				fmt.Fprintf(os.Stderr, "Would permanently purge tombstoned message %s in %s.\n", sanitize(id), sanitize(chat))
				return nil
			}
			if !confirm {
				fmt.Fprintf(os.Stderr, "Permanently purge retained payload for message %s in %s? This cannot be undone. [y/N] ", sanitize(id), sanitize(chat))
				answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
				answer = strings.TrimSpace(strings.ToLower(answer))
				if answer != "y" && answer != "yes" {
					fmt.Fprintln(os.Stderr, "Aborted.")
					return nil
				}
			}
			if _, err := deleteLocalMediaIfRequested(true, msg.LocalPath); err != nil {
				return fmt.Errorf("delete retained local media: %w", err)
			}
			if err := a.DB().PurgeMessage(chat, id); err != nil {
				if errors.Is(err, store.ErrMessageNotTombstoned) {
					return store.ErrMessageNotTombstoned
				}
				return err
			}
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{"purged": 1, "chat": chat, "id": id})
			}
			fmt.Fprintf(os.Stderr, "Purged message %s in %s.\n", sanitize(id), sanitize(chat))
			return nil
		},
	}
	cmd.Flags().StringVar(&chat, "chat", "", "chat JID")
	cmd.Flags().StringVar(&id, "id", "", "tombstoned message ID")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show the retained payload without purging it")
	cmd.Flags().BoolVar(&confirm, "confirm", false, "skip confirmation prompt")
	return cmd
}

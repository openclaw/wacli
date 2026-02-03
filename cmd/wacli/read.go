package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	appPkg "github.com/steipete/wacli/internal/app"
	"github.com/steipete/wacli/internal/out"
	"github.com/steipete/wacli/internal/wa"
	"go.mau.fi/whatsmeow/types"
)

func newReadCmd(flags *rootFlags) *cobra.Command {
	var chat string
	var messageIDs []string

	cmd := &cobra.Command{
		Use:   "read",
		Short: "Send read receipts for messages",
		Long:  "Mark specified messages as read in a chat. Requires an active connection or a running sync process.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if chat == "" || len(messageIDs) == 0 {
				return fmt.Errorf("--chat and at least one --message are required")
			}

			// Clean up message IDs.
			var cleanIDs []string
			for _, id := range messageIDs {
				id = strings.TrimSpace(id)
				if id != "" {
					cleanIDs = append(cleanIDs, id)
				}
			}
			if len(cleanIDs) == 0 {
				return fmt.Errorf("at least one non-empty --message is required")
			}

			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			storeDir := resolveStoreDir(flags)

			// Try socket first (sync process is running).
			if appPkg.IsSocketAvailable(storeDir) {
				resp, err := appPkg.SendSocketRequest(storeDir, appPkg.SocketRequest{
					Action:     "mark_read",
					Chat:       chat,
					MessageIDs: cleanIDs,
				})
				if err != nil {
					return fmt.Errorf("socket request failed: %w", err)
				}
				if !resp.OK {
					return fmt.Errorf("mark read failed: %s", resp.Error)
				}
				if flags.asJSON {
					return out.WriteJSON(os.Stdout, map[string]any{
						"read":        true,
						"chat":        chat,
						"message_ids": cleanIDs,
					})
				}
				fmt.Fprintf(os.Stdout, "Marked %d message(s) as read in %s\n", len(cleanIDs), chat)
				return nil
			}

			// Fallback: direct connection.
			a, lk, err := newApp(ctx, flags, true, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			if err := a.EnsureAuthed(); err != nil {
				return err
			}
			if err := a.Connect(ctx, false, nil); err != nil {
				return err
			}

			chatJID, err := wa.ParseUserOrJID(chat)
			if err != nil {
				return fmt.Errorf("invalid chat JID: %w", err)
			}

			msgIDs := make([]types.MessageID, len(cleanIDs))
			for i, id := range cleanIDs {
				msgIDs[i] = types.MessageID(id)
			}

			if err := a.WA().MarkRead(ctx, chatJID, msgIDs); err != nil {
				return fmt.Errorf("mark read failed: %w", err)
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{
					"read":        true,
					"chat":        chatJID.String(),
					"message_ids": cleanIDs,
				})
			}
			fmt.Fprintf(os.Stdout, "Marked %d message(s) as read in %s\n", len(cleanIDs), chatJID.String())
			return nil
		},
	}

	cmd.Flags().StringVar(&chat, "chat", "", "chat JID or phone number")
	cmd.Flags().StringArrayVar(&messageIDs, "message", nil, "message ID(s) to mark as read (repeatable)")
	return cmd
}

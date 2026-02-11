package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/steipete/wacli/internal/config"
	"github.com/steipete/wacli/internal/ipc"
	"github.com/steipete/wacli/internal/out"
	"github.com/steipete/wacli/internal/wa"
)

func newDeleteCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete messages",
	}
	cmd.AddCommand(newDeleteMessageCmd(flags))
	return cmd
}

func newDeleteMessageCmd(flags *rootFlags) *cobra.Command {
	var chat string
	var msgID string
	var forEveryone bool
	var noIPC bool

	cmd := &cobra.Command{
		Use:   "message",
		Short: "Delete a message (revoke)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if chat == "" || msgID == "" {
				return fmt.Errorf("--chat and --id are required")
			}

			// Resolve store directory
			storeDir := flags.storeDir
			if storeDir == "" {
				storeDir = config.DefaultStoreDir()
			}
			storeDir, _ = filepath.Abs(storeDir)

			// Try IPC first if not disabled
			if !noIPC {
				client := ipc.NewClient(storeDir)
				if client.IsAvailable() {
					err := client.DeleteMessage(chat, msgID, forEveryone)
					if err != nil {
						fmt.Fprintf(os.Stderr, "IPC delete failed (%v), trying direct mode...\n", err)
					} else {
						if flags.asJSON {
							return out.WriteJSON(os.Stdout, map[string]any{
								"deleted":     true,
								"chat":        chat,
								"id":          msgID,
								"forEveryone": forEveryone,
								"via":         "ipc",
							})
						}
						fmt.Fprintf(os.Stdout, "Deleted message %s from %s via daemon\n", msgID, chat)
						return nil
					}
				}
			}

			// Direct mode
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

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
				return fmt.Errorf("parse chat: %w", err)
			}

			// Type assert to get the concrete client for RevokeMessage
			waClient, ok := a.WA().(*wa.Client)
			if !ok {
				return fmt.Errorf("unexpected WA client type")
			}

			if err := waClient.RevokeMessage(ctx, chatJID, msgID, forEveryone); err != nil {
				return fmt.Errorf("revoke: %w", err)
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{
					"deleted":     true,
					"chat":        chatJID.String(),
					"id":          msgID,
					"forEveryone": forEveryone,
					"via":         "direct",
				})
			}
			fmt.Fprintf(os.Stdout, "Deleted message %s from %s\n", msgID, chatJID.String())
			return nil
		},
	}

	cmd.Flags().StringVar(&chat, "chat", "", "chat JID (e.g., 1234567890@s.whatsapp.net)")
	cmd.Flags().StringVar(&msgID, "id", "", "message ID to delete")
	cmd.Flags().BoolVar(&forEveryone, "for-everyone", true, "delete for everyone (not just locally)")
	cmd.Flags().BoolVar(&noIPC, "no-ipc", false, "skip IPC and use direct connection")
	return cmd
}

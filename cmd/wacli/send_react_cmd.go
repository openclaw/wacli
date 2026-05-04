package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steipete/wacli/internal/out"
	"github.com/steipete/wacli/internal/wa"
	"go.mau.fi/whatsmeow/types"
)

func newSendReactCmd(flags *rootFlags) *cobra.Command {
	var to string
	var msgID string
	var emoji string
	var sender string

	cmd := &cobra.Command{
		Use:   "react",
		Short: "React to a message",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(to) == "" || strings.TrimSpace(msgID) == "" {
				return fmt.Errorf("--to and --id are required")
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

			if err := a.EnsureAuthed(); err != nil {
				return err
			}
			if err := a.Connect(ctx, false, nil); err != nil {
				return err
			}

			chat, senderJID, err := reactionTarget(to, sender)
			if err != nil {
				return err
			}
			sentID, err := runSendOperation(ctx, reconnectForSend(a), func(ctx context.Context) (types.MessageID, error) {
				return a.WA().SendReaction(ctx, chat, senderJID, types.MessageID(msgID), emoji)
			})
			if err != nil {
				return err
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{
					"sent":     true,
					"to":       chat.String(),
					"id":       sentID,
					"target":   msgID,
					"reaction": emoji,
				})
			}
			if emoji == "" {
				fmt.Fprintf(os.Stdout, "Removed reaction from %s (id %s)\n", msgID, sentID)
				return nil
			}
			fmt.Fprintf(os.Stdout, "Reacted %s to %s (id %s)\n", emoji, msgID, sentID)
			return nil
		},
	}

	cmd.Flags().StringVar(&to, "to", "", "recipient phone number (+E164 and formatting ok) or JID")
	cmd.Flags().StringVar(&msgID, "id", "", "target message ID")
	cmd.Flags().StringVar(&emoji, "reaction", "\U0001f44d", "reaction emoji (pass an empty string to remove)")
	cmd.Flags().StringVar(&sender, "sender", "", "message sender JID (required for group messages)")
	return cmd
}

func reactionTarget(to, sender string) (types.JID, types.JID, error) {
	chat, err := wa.ParseUserOrJID(to)
	if err != nil {
		return types.JID{}, types.JID{}, fmt.Errorf("invalid --to: %w", err)
	}
	var senderJID types.JID
	if strings.TrimSpace(sender) != "" {
		senderJID, err = wa.ParseUserOrJID(sender)
		if err != nil {
			return types.JID{}, types.JID{}, fmt.Errorf("invalid --sender: %w", err)
		}
	}
	if chat.Server == types.GroupServer && senderJID.IsEmpty() {
		return types.JID{}, types.JID{}, fmt.Errorf("--sender is required for group reactions")
	}
	return chat, senderJID, nil
}

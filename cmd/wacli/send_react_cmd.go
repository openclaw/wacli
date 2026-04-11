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
		Short: "React to a message with an emoji",
		Long: `Send an emoji reaction to a specific WhatsApp message.

Requires the conversation JID (--to) and the target message ID (--id).
For group messages, also pass the message author's JID via --sender.

To remove an existing reaction, pass an empty --reaction flag:

  wacli send react --to +15551234567 --id MSGID --reaction ""`,
		Example: `  # React with 👍 (default)
  wacli send react --to +15551234567 --id 3EB0A3DCA3175E17850852

  # React with a custom emoji
  wacli send react --to +15551234567 --id 3EB0A3DCA3175E17850852 --reaction 🔥

  # React in a group (sender JID required)
  wacli send react --to 1234567890-123456789@g.us --id MSGID --sender +15559876543 --reaction ❤️

  # Remove a reaction
  wacli send react --to +15551234567 --id MSGID --reaction ""`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(to) == "" {
				return fmt.Errorf("--to is required")
			}
			if strings.TrimSpace(msgID) == "" {
				return fmt.Errorf("--id is required")
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

			toJID, err := wa.ParseUserOrJID(to)
			if err != nil {
				return fmt.Errorf("invalid --to: %w", err)
			}

			var senderJID types.JID
			if strings.TrimSpace(sender) != "" {
				senderJID, err = wa.ParseUserOrJID(sender)
				if err != nil {
					return fmt.Errorf("invalid --sender: %w", err)
				}
			}

			sentID, err := a.WA().SendReaction(ctx, toJID, senderJID, types.MessageID(msgID), emoji)
			if err != nil {
				return err
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{
					"sent":     true,
					"to":       toJID.String(),
					"id":       sentID,
					"target":   msgID,
					"reaction": emoji,
				})
			}

			if emoji == "" {
				fmt.Fprintf(os.Stdout, "Removed reaction from message %s (sent id %s)\n", msgID, sentID)
			} else {
				fmt.Fprintf(os.Stdout, "Reacted %s to message %s (sent id %s)\n", emoji, msgID, sentID)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&to, "to", "", "recipient phone number or JID")
	cmd.Flags().StringVar(&msgID, "id", "", "target message ID to react to")
	cmd.Flags().StringVar(&emoji, "reaction", "👍", `reaction emoji (pass "" to remove an existing reaction)`)
	cmd.Flags().StringVar(&sender, "sender", "", "message author JID (required for group messages)")
	return cmd
}

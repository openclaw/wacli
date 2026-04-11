package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steipete/wacli/internal/out"
	"github.com/steipete/wacli/internal/wa"
)

func newSendReactCmd(flags *rootFlags) *cobra.Command {
	var to string
	var messageID string
	var emoji string

	cmd := &cobra.Command{
		Use:   "react",
		Short: "Send a reaction to a message (or remove one with empty emoji)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if to == "" || messageID == "" {
				return fmt.Errorf("--to and --id are required")
			}
			if !cmd.Flags().Changed("emoji") {
				return fmt.Errorf("--emoji is required (use empty string to remove a reaction)")
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
				return err
			}

			msgID, err := sendReact(ctx, a, toJID, messageID, emoji)
			if err != nil {
				return err
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{
					"sent":       true,
					"to":         toJID.String(),
					"id":         msgID,
					"target_id":  messageID,
					"emoji":      emoji,
				})
			}
			if emoji == "" {
				fmt.Fprintf(os.Stdout, "Removed reaction on %s in %s (id %s)\n", messageID, toJID.String(), msgID)
			} else {
				fmt.Fprintf(os.Stdout, "Reacted %s on %s in %s (id %s)\n", emoji, messageID, toJID.String(), msgID)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&to, "to", "", "recipient phone number or JID (chat where the target message is)")
	cmd.Flags().StringVar(&messageID, "id", "", "message ID to react to")
	cmd.Flags().StringVar(&emoji, "emoji", "", "reaction emoji (empty string to remove)")
	return cmd
}

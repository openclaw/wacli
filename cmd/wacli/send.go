package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/steipete/wacli/internal/linkpreview"
	"github.com/steipete/wacli/internal/out"
	"github.com/steipete/wacli/internal/store"
	"github.com/steipete/wacli/internal/wa"
)

func newSendCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "send",
		Short: "Send messages",
	}
	cmd.AddCommand(newSendTextCmd(flags))
	cmd.AddCommand(newSendFileCmd(flags))
	return cmd
}

func newSendTextCmd(flags *rootFlags) *cobra.Command {
	var to string
	var message string
	var noPreview bool

	cmd := &cobra.Command{
		Use:   "text",
		Short: "Send a text message",
		RunE: func(cmd *cobra.Command, args []string) error {
			if to == "" || message == "" {
				return fmt.Errorf("--to and --message are required")
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

			var msgID string

			// Try to fetch link preview if the message contains a URL.
			if !noPreview {
				if rawURL := linkpreview.FindURL(message); rawURL != "" {
					previewCtx, previewCancel := context.WithTimeout(ctx, 10*time.Second)
					preview, previewErr := linkpreview.Fetch(previewCtx, rawURL)
					previewCancel()
					if previewErr == nil && preview != nil {
						lp := &wa.LinkPreview{
							URL:         preview.URL,
							Title:       preview.Title,
							Description: preview.Description,
							Thumbnail:   preview.Thumbnail,
						}
						msgID, err = a.WA().SendTextWithPreview(ctx, toJID, message, lp)
					}
				}
			}

			// Fallback to plain text if no preview was fetched or preview send failed.
			if msgID == "" && err == nil {
				msgID, err = a.WA().SendText(ctx, toJID, message)
			}
			if err != nil {
				return err
			}

			now := time.Now().UTC()
			chat := toJID
			chatName := a.WA().ResolveChatName(ctx, chat, "")
			kind := chatKindFromJID(chat)
			_ = a.DB().UpsertChat(chat.String(), kind, chatName, now)
			_ = a.DB().UpsertMessage(store.UpsertMessageParams{
				ChatJID:    chat.String(),
				ChatName:   chatName,
				MsgID:      string(msgID),
				SenderJID:  "",
				SenderName: "me",
				Timestamp:  now,
				FromMe:     true,
				Text:       message,
			})

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{
					"sent": true,
					"to":   chat.String(),
					"id":   msgID,
				})
			}
			fmt.Fprintf(os.Stdout, "Sent to %s (id %s)\n", chat.String(), msgID)
			return nil
		},
	}

	cmd.Flags().StringVar(&to, "to", "", "recipient phone number or JID")
	cmd.Flags().StringVar(&message, "message", "", "message text")
	cmd.Flags().BoolVar(&noPreview, "no-preview", false, "disable link preview")
	return cmd
}

package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/store"
	"github.com/spf13/cobra"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

func newMessagesEditCmd(flags *rootFlags) *cobra.Command {
	var chat string
	var id string
	var message string
	postSendWait := postSendRetryReceiptWait

	cmd := &cobra.Command{
		Use:   "edit",
		Short: "Edit one of your recent sent text messages",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(chat) == "" || strings.TrimSpace(id) == "" || strings.TrimSpace(message) == "" {
				return fmt.Errorf("--chat, --id, and --message are required")
			}
			if err := flags.requireWritable(); err != nil {
				return err
			}

			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, true, false)
			if err != nil {
				resp, delegated, delegateErr := tryDelegateSend(ctx, flags, err, sendDelegateRequest{
					Kind:           "edit",
					To:             chat,
					ID:             id,
					Message:        message,
					PostSendWaitMS: durationMillis(postSendWait),
				})
				if delegated {
					if delegateErr != nil {
						return delegateErr
					}
					if flags.asJSON {
						return out.WriteJSON(os.Stdout, map[string]any{
							"edited":  true,
							"to":      resp.To,
							"id":      resp.ID,
							"target":  resp.Target,
							"message": message,
						})
					}
					fmt.Fprintf(os.Stdout, "Edited message %s in %s (id %s)\n", resp.Target, resp.To, resp.ID)
					return nil
				}
				return err
			}
			defer closeApp(a, lk)

			if err := a.EnsureAuthed(ctx); err != nil {
				return err
			}
			msg, chatJID, err := loadMessageMutationTarget(ctx, a, chat, id)
			if err != nil {
				return err
			}
			if err := validateMessageCanEdit(msg, time.Now().UTC()); err != nil {
				return err
			}
			if err := a.Connect(ctx, false, nil); err != nil {
				return err
			}
			if err := warnRapidSendIfNeeded(a.StoreDir(), time.Now().UTC(), os.Stderr); err != nil {
				return err
			}
			sentID, err := runSendOperation(ctx, reconnectForSend(a), func(ctx context.Context) (types.MessageID, error) {
				return a.WA().EditMessage(ctx, chatJID, types.MessageID(msg.MsgID), message)
			})
			if err != nil {
				return err
			}
			if err := a.DB().UpdateMessageText(msg.ChatJID, msg.MsgID, message); err != nil {
				return fmt.Errorf("store edited message text: %w", err)
			}

			waitForPostSendRetryReceipts(ctx, postSendWait)

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{
					"edited":  true,
					"to":      chatJID.String(),
					"id":      sentID,
					"target":  msg.MsgID,
					"message": message,
				})
			}
			fmt.Fprintf(os.Stdout, "Edited message %s in %s (id %s)\n", msg.MsgID, chatJID.String(), sentID)
			return nil
		},
	}
	cmd.Flags().StringVar(&chat, "chat", "", "chat JID, phone number, or contact/group/chat name")
	cmd.Flags().StringVar(&id, "id", "", "message ID to edit")
	cmd.Flags().StringVar(&message, "message", "", "new message text")
	cmd.Flags().DurationVar(&postSendWait, "post-send-wait", postSendRetryReceiptWait, "keep the connection alive after edit so retry receipts can be handled (0 disables)")
	return cmd
}

func validateMessageCanEdit(msg store.Message, now time.Time) error {
	if err := validateMessageCanRevoke(msg); err != nil {
		return err
	}
	if strings.TrimSpace(msg.MediaType) != "" {
		return fmt.Errorf("only text messages can be edited")
	}
	if strings.TrimSpace(msg.Text) == "" && strings.TrimSpace(msg.DisplayText) == "" {
		return fmt.Errorf("only text messages can be edited")
	}
	if !msg.Timestamp.IsZero() && now.Sub(msg.Timestamp) > whatsmeow.EditWindow {
		return fmt.Errorf("message %s is older than WhatsApp's %s edit window", msg.MsgID, whatsmeow.EditWindow)
	}
	return nil
}

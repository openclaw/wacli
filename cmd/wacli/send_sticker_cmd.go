package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/openclaw/wacli/internal/out"
	"github.com/spf13/cobra"
)

func newSendStickerCmd(flags *rootFlags) *cobra.Command {
	var to string
	var pick int
	var filePath string
	var replyTo string
	var replyToSender string
	postSendWait := postSendRetryReceiptWait

	cmd := &cobra.Command{
		Use:   "sticker",
		Short: "Send a sticker (WebP image)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if to == "" || filePath == "" {
				return fmt.Errorf("--to and --file are required")
			}
			if err := flags.requireWritable(); err != nil {
				return err
			}

			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, true, false)
			if err != nil {
				delegateFile := filePath
				if abs, absErr := filepath.Abs(filePath); absErr == nil {
					delegateFile = abs
				}
				resp, delegated, delegateErr := tryDelegateSend(ctx, flags, err, sendDelegateRequest{
					Kind:           "sticker",
					To:             to,
					Pick:           pick,
					File:           delegateFile,
					ReplyTo:        replyTo,
					ReplyToSender:  replyToSender,
					PostSendWaitMS: durationMillis(postSendWait),
				})
				if delegated {
					if delegateErr != nil {
						return delegateErr
					}
					return writeDelegatedSendOutput(flags, "sticker", resp)
				}
				return err
			}
			defer closeApp(a, lk)

			if err := a.EnsureAuthed(); err != nil {
				return err
			}

			toJID, err := resolveRecipient(a, to, recipientOptions{pick: pick, asJSON: flags.asJSON})
			if err != nil {
				return err
			}
			if err := a.Connect(ctx, false, nil); err != nil {
				return err
			}
			if err := warnRapidSendIfNeeded(a.StoreDir(), time.Now().UTC(), os.Stderr); err != nil {
				return err
			}

			type sendStickerResult struct {
				id   string
				meta map[string]string
			}
			res, err := runSendOperation(ctx, reconnectForSend(a), func(ctx context.Context) (sendStickerResult, error) {
				msgID, meta, err := sendSticker(ctx, a, toJID, filePath, sendStickerOptions{
					replyTo:       replyTo,
					replyToSender: replyToSender,
				})
				if err != nil {
					return sendStickerResult{}, err
				}
				return sendStickerResult{id: msgID, meta: meta}, nil
			})
			if err != nil {
				return err
			}

			waitForPostSendRetryReceipts(ctx, postSendWait)

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{
					"sent": true,
					"to":   toJID.String(),
					"id":   res.id,
					"file": res.meta,
				})
			}
			fmt.Fprintf(os.Stdout, "Sent sticker to %s (id %s)\n", toJID.String(), res.id)
			return nil
		},
	}

	cmd.Flags().StringVar(&to, "to", "", "recipient JID, phone number, or contact/group/chat name")
	cmd.Flags().IntVar(&pick, "pick", 0, "when --to is ambiguous, pick the Nth match (1-indexed)")
	cmd.Flags().StringVar(&filePath, "file", "", "path to WebP sticker file")
	cmd.Flags().StringVar(&replyTo, "reply-to", "", "message ID to quote/reply to")
	cmd.Flags().StringVar(&replyToSender, "reply-to-sender", "", "sender JID of the quoted message (required for unsynced group replies)")
	cmd.Flags().DurationVar(&postSendWait, "post-send-wait", postSendRetryReceiptWait, "keep the connection alive after send so retry receipts can be handled (0 disables)")
	return cmd
}

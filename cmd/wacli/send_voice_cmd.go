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

func newSendVoiceCmd(flags *rootFlags) *cobra.Command {
	var to string
	var pick int
	var filePath string
	var mimeOverride string
	var replyTo string
	var replyToSender string
	postSendWait := postSendRetryReceiptWait

	cmd := &cobra.Command{
		Use:   "voice",
		Short: "Send a voice note",
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
					Kind:           "voice",
					To:             to,
					Pick:           pick,
					File:           delegateFile,
					MIME:           mimeOverride,
					ReplyTo:        replyTo,
					ReplyToSender:  replyToSender,
					PostSendWaitMS: durationMillis(postSendWait),
				})
				if delegated {
					if delegateErr != nil {
						return delegateErr
					}
					return writeDelegatedSendOutput(flags, "voice", resp)
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

			type sendVoiceResult struct {
				id   string
				meta map[string]string
			}
			res, err := runSendOperation(ctx, reconnectForSend(a), func(ctx context.Context) (sendVoiceResult, error) {
				msgID, meta, err := sendFile(ctx, a, toJID, filePath, sendFileOptions{
					mimeOverride:  mimeOverride,
					replyTo:       replyTo,
					replyToSender: replyToSender,
					ptt:           true,
				})
				if err != nil {
					return sendVoiceResult{}, err
				}
				return sendVoiceResult{id: msgID, meta: meta}, nil
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
			fmt.Fprintf(os.Stdout, "Sent voice note to %s (id %s)\n", toJID.String(), res.id)
			return nil
		},
	}

	cmd.Flags().StringVar(&to, "to", "", "recipient JID, phone number, or contact/group/chat name")
	cmd.Flags().IntVar(&pick, "pick", 0, "when --to is ambiguous, pick the Nth match (1-indexed)")
	cmd.Flags().StringVar(&filePath, "file", "", "path to OGG/Opus audio file")
	cmd.Flags().StringVar(&mimeOverride, "mime", "", "override detected mime type")
	cmd.Flags().StringVar(&replyTo, "reply-to", "", "message ID to quote/reply to")
	cmd.Flags().StringVar(&replyToSender, "reply-to-sender", "", "sender JID of the quoted message (required for unsynced group replies)")
	cmd.Flags().DurationVar(&postSendWait, "post-send-wait", postSendRetryReceiptWait, "keep the connection alive after send so retry receipts can be handled (0 disables)")
	return cmd
}

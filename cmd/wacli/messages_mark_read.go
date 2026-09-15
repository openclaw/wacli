package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"go.mau.fi/whatsmeow/types"

	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/wa"
)

func newMessagesMarkReadCmd(flags *rootFlags) *cobra.Command {
	var chat string
	var ids []string
	var sender string
	var pick int

	cmd := &cobra.Command{
		Use:   "mark-read",
		Short: "Send read receipts for received messages (blue ticks, when read receipts are on)",
		Long: `Send WhatsApp read receipts for one or more received messages in a chat, so
their sender sees them as read.

Receipts follow this account's read-receipts privacy setting: when it is off,
WhatsApp sends a "read-self" receipt instead, which only syncs to your own
devices and never shows the sender blue ticks (in groups too). The result
reports which one was sent ("receipt": "read" or "read-self").

This is different from "chats mark-read", which only syncs a chat's read flag to
your own linked devices. In a group chat the receipt goes to the participant who
sent the messages: pass --sender, or leave it out to look the sender up in the
local store (every --id must then come from the same participant). Messages the
local store knows as sent by you are rejected.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(chat) == "" {
				return fmt.Errorf("--chat is required")
			}
			cleanIDs := cleanMessageIDs(ids)
			if len(cleanIDs) == 0 {
				return fmt.Errorf("--id is required")
			}
			if err := flags.requireWritable(); err != nil {
				return err
			}

			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, true, false)
			if err != nil {
				resp, delegated, delegateErr := tryDelegateSend(ctx, flags, err, sendDelegateRequest{
					Kind:   "read_receipt",
					To:     chat,
					Pick:   pick,
					IDs:    cleanIDs,
					Sender: sender,
				})
				if delegated {
					if delegateErr != nil {
						return delegateErr
					}
					return writeMarkReadOutput(flags, os.Stdout, os.Stderr, resp.Chat, resp.IDs, types.ReceiptType(resp.Receipt))
				}
				return err
			}
			defer closeApp(a, lk)

			if err := a.EnsureAuthed(ctx); err != nil {
				return err
			}
			if err := a.Connect(ctx, false, nil); err != nil {
				return err
			}

			chatJID, err := resolveRecipient(a, chat, recipientOptions{pick: pick, asJSON: flags.asJSON})
			if err != nil {
				return err
			}
			senderJID, err := parseOptionalSender(sender)
			if err != nil {
				return err
			}
			receipt, err := a.MarkMessagesRead(ctx, chatJID, cleanIDs, senderJID)
			if err != nil {
				return err
			}
			return writeMarkReadOutput(flags, os.Stdout, os.Stderr, chatJID.String(), cleanIDs, receipt)
		},
	}

	cmd.Flags().StringVar(&chat, "chat", "", "chat JID, phone number, or contact/group/chat name")
	cmd.Flags().StringArrayVar(&ids, "id", nil, "message ID to mark as read (repeatable)")
	cmd.Flags().StringVar(&sender, "sender", "", "sender JID of the messages (group chats; defaults to the local store)")
	cmd.Flags().IntVar(&pick, "pick", 0, "when --chat is ambiguous, pick the Nth match (1-indexed)")
	return cmd
}

func cleanMessageIDs(ids []string) []string {
	cleaned := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id != "" {
			cleaned = append(cleaned, id)
		}
	}
	return cleaned
}

func parseOptionalSender(sender string) (types.JID, error) {
	if strings.TrimSpace(sender) == "" {
		return types.JID{}, nil
	}
	jid, err := wa.ParseUserOrJID(sender)
	if err != nil {
		return types.JID{}, fmt.Errorf("invalid --sender: %w", err)
	}
	return jid, nil
}

const readSelfHint = "read receipts are off in this account's WhatsApp privacy settings, so a read-self receipt was sent: the sender will not see blue ticks"

// writeMarkReadOutput prints the command result. When the receipt was
// read-self, the hint goes out as a "warning" NDJSON event under --events (so
// the stderr stream stays machine-readable) and as a plain note otherwise.
func writeMarkReadOutput(flags *rootFlags, stdout, stderr io.Writer, chat string, ids []string, receipt types.ReceiptType) error {
	senderNotified := receipt == types.ReceiptTypeRead
	if !senderNotified {
		if flags.events {
			_ = out.NewEventWriter(stderr, true).Emit("warning", map[string]any{
				"code": "read_self_receipt", "message": readSelfHint, "chat": chat, "receipt": string(receipt),
			})
		} else if !flags.asJSON {
			fmt.Fprintf(stderr, "Note: %s\n", readSelfHint)
		}
	}
	if flags.asJSON {
		return out.WriteJSON(stdout, map[string]any{
			"sent":            true,
			"chat":            chat,
			"ids":             ids,
			"receipt":         string(receipt),
			"sender_notified": senderNotified,
		})
	}
	if senderNotified {
		fmt.Fprintf(stdout, "Marked %d message(s) as read in %s\n", len(ids), chat)
	} else {
		fmt.Fprintf(stdout, "Sent %s receipt(s) for %d message(s) in %s\n", receipt, len(ids), chat)
	}
	return nil
}

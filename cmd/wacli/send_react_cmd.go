package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/wa"
	"github.com/spf13/cobra"
	"go.mau.fi/whatsmeow/types"
)

func newSendReactCmd(flags *rootFlags) *cobra.Command {
	var to string
	var msgID string
	var emoji string
	var sender string
	postSendWait := postSendRetryReceiptWait

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
				resp, delegated, delegateErr := tryDelegateSend(ctx, flags, err, sendDelegateRequest{
					Kind:           "react",
					To:             to,
					ID:             msgID,
					Reaction:       emoji,
					Sender:         sender,
					PostSendWaitMS: durationMillis(postSendWait),
				})
				if delegated {
					if delegateErr != nil {
						return delegateErr
					}
					return writeDelegatedSendOutput(flags, "react", resp)
				}
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
			if err := warnRapidSendIfNeeded(a.StoreDir(), time.Now().UTC(), os.Stderr); err != nil {
				return err
			}
			sentID, err := runSendOperation(ctx, reconnectForSend(a), func(ctx context.Context) (types.MessageID, error) {
				return a.WA().SendReaction(ctx, chat, senderJID, types.MessageID(msgID), emoji)
			})
			if err != nil {
				return err
			}

			now := time.Now().UTC()
			chatName := a.WA().ResolveChatName(ctx, chat, "")
			upsertSentReaction(a.DB(), chat, chatName, sentID, msgID, emoji, now)

			waitForPostSendRetryReceipts(ctx, postSendWait)

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
	cmd.Flags().DurationVar(&postSendWait, "post-send-wait", postSendRetryReceiptWait, "keep the connection alive after send so retry receipts can be handled (0 disables)")
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

func upsertSentReaction(db *store.DB, chat types.JID, chatName string, sentID types.MessageID, targetID, emoji string, now time.Time) {
	if db == nil || chat.IsEmpty() || sentID == "" {
		return
	}
	_ = db.UpsertChat(chat.String(), chatKindFromJID(chat), chatName, now)
	_ = db.UpsertMessage(store.UpsertMessageParams{
		ChatJID:       chat.String(),
		ChatName:      chatName,
		MsgID:         string(sentID),
		SenderName:    "me",
		Timestamp:     now,
		FromMe:        true,
		DisplayText:   sentReactionDisplayText(db, chat.String(), targetID, emoji),
		ReactionToID:  targetID,
		ReactionEmoji: emoji,
	})
}

func sentReactionDisplayText(db *store.DB, chatJID, targetID, emoji string) string {
	display := "message"
	if db != nil && strings.TrimSpace(chatJID) != "" && strings.TrimSpace(targetID) != "" {
		if msg, err := db.GetMessage(chatJID, targetID); err == nil {
			if text := strings.TrimSpace(messageText(msg)); text != "" {
				display = text
			}
		}
	}
	if strings.TrimSpace(emoji) == "" {
		return fmt.Sprintf("Reacted to %s", display)
	}
	return fmt.Sprintf("Reacted %s to %s", emoji, display)
}

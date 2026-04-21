package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steipete/wacli/internal/app"
	"github.com/steipete/wacli/internal/out"
	"github.com/steipete/wacli/internal/store"
	"github.com/steipete/wacli/internal/wa"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
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
	var replyTo string
	var replyToSender string

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

			msgID, err := sendTextMessage(ctx, a, toJID, message, replyTo, replyToSender)
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
	cmd.Flags().StringVar(&replyTo, "reply-to", "", "message ID to quote/reply to")
	cmd.Flags().StringVar(&replyToSender, "reply-to-sender", "", "sender JID of the quoted message (required for unsynced group replies)")
	return cmd
}

type sendTextApp interface {
	WA() app.WAClient
	DB() *store.DB
}

func sendTextMessage(ctx context.Context, a sendTextApp, to types.JID, text, replyTo, replyToSender string) (types.MessageID, error) {
	replyTo = strings.TrimSpace(replyTo)
	if replyTo == "" {
		return a.WA().SendText(ctx, to, text)
	}

	sender, err := resolveReplySender(a.DB(), to, replyTo, replyToSender)
	if err != nil {
		return "", err
	}

	stanzaID := replyTo
	info := &waProto.ContextInfo{StanzaID: proto.String(stanzaID)}
	if !sender.IsEmpty() {
		participant := sender.String()
		info.Participant = proto.String(participant)
	}

	return a.WA().SendProtoMessage(ctx, to, &waProto.Message{
		ExtendedTextMessage: &waProto.ExtendedTextMessage{
			Text:        proto.String(text),
			ContextInfo: info,
		},
	})
}

func resolveReplySender(db *store.DB, chat types.JID, replyTo, override string) (types.JID, error) {
	if strings.TrimSpace(override) != "" {
		jid, err := wa.ParseUserOrJID(override)
		if err != nil {
			return types.JID{}, fmt.Errorf("invalid --reply-to-sender: %w", err)
		}
		return jid, nil
	}

	msg, err := db.GetMessage(chat.String(), replyTo)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return types.JID{}, fmt.Errorf("lookup quoted message: %w", err)
	}
	if err == nil && strings.TrimSpace(msg.SenderJID) != "" {
		jid, err := types.ParseJID(msg.SenderJID)
		if err != nil {
			return types.JID{}, fmt.Errorf("stored quoted sender is invalid: %w", err)
		}
		return jid, nil
	}

	if chat.Server == types.GroupServer {
		return types.JID{}, fmt.Errorf("--reply-to-sender is required for unsynced group replies")
	}
	return types.JID{}, nil
}

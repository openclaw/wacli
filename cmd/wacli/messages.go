package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/wa"
	"github.com/spf13/cobra"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

func newMessagesCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "messages",
		Short: "List and search messages from the local DB",
	}
	cmd.AddCommand(newMessagesListCmd(flags))
	cmd.AddCommand(newMessagesSearchCmd(flags))
	cmd.AddCommand(newMessagesStarredCmd(flags))
	cmd.AddCommand(newMessagesShowCmd(flags))
	cmd.AddCommand(newMessagesContextCmd(flags))
	cmd.AddCommand(newMessagesExportCmd(flags))
	cmd.AddCommand(newMessagesDeleteCmd(flags))
	cmd.AddCommand(newMessagesEditCmd(flags))
	return cmd
}

func newMessagesListCmd(flags *rootFlags) *cobra.Command {
	var chat string
	var sender string
	var limit int
	var afterStr string
	var beforeStr string
	var fromMe bool
	var fromThem bool
	var asc bool
	var forwarded bool
	var starred bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List messages",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			if fromMe && fromThem {
				return fmt.Errorf("--from-me and --from-them are mutually exclusive")
			}

			a, lk, err := newApp(ctx, flags, false, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			var after *time.Time
			var before *time.Time
			if afterStr != "" {
				t, err := parseTime(afterStr)
				if err != nil {
					return err
				}
				after = &t
			}
			if beforeStr != "" {
				t, err := parseTime(beforeStr)
				if err != nil {
					return err
				}
				before = &t
			}

			var fromMeFilter *bool
			switch {
			case fromMe:
				v := true
				fromMeFilter = &v
			case fromThem:
				v := false
				fromMeFilter = &v
			}

			chatJIDs, err := messageChatJIDFilter(ctx, a, chat)
			if err != nil {
				return err
			}

			msgs, err := a.DB().ListMessages(store.ListMessagesParams{
				ChatJIDs:  chatJIDs,
				SenderJID: sender,
				Limit:     limit,
				After:     after,
				Before:    before,
				FromMe:    fromMeFilter,
				Asc:       asc,
				Forwarded: forwarded,
				Starred:   starred,
			})
			if err != nil {
				return err
			}
			msgs = resolveMessageSenderNames(ctx, a, msgs)

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{
					"messages": msgs,
					"fts":      a.DB().HasFTS(),
				})
			}

			return writeMessagesList(os.Stdout, msgs, fullTableOutput(flags.fullOutput))
		},
	}

	cmd.Flags().StringVar(&chat, "chat", "", "filter by chat JID")
	cmd.Flags().StringVar(&sender, "sender", "", "filter by sender JID")
	cmd.Flags().IntVar(&limit, "limit", 50, "max number of messages to return")
	cmd.Flags().StringVar(&afterStr, "after", "", "only messages after time (RFC3339 or YYYY-MM-DD)")
	cmd.Flags().StringVar(&beforeStr, "before", "", "only messages before time (RFC3339 or YYYY-MM-DD)")
	cmd.Flags().BoolVar(&fromMe, "from-me", false, "only messages sent by me")
	cmd.Flags().BoolVar(&fromThem, "from-them", false, "only messages received (not sent by me)")
	cmd.Flags().BoolVar(&asc, "asc", false, "show oldest messages first (default: newest first)")
	cmd.Flags().BoolVar(&forwarded, "forwarded", false, "only forwarded messages")
	cmd.Flags().BoolVar(&starred, "starred", false, "only starred messages")
	return cmd
}

func newMessagesSearchCmd(flags *rootFlags) *cobra.Command {
	var chat string
	var from string
	var limit int
	var afterStr string
	var beforeStr string
	var hasMedia bool
	var msgType string
	var forwarded bool
	var starred bool

	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search messages (FTS5 if available; otherwise LIKE)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, false, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			var after *time.Time
			var before *time.Time
			if afterStr != "" {
				t, err := parseTime(afterStr)
				if err != nil {
					return err
				}
				after = &t
			}
			if beforeStr != "" {
				t, err := parseTime(beforeStr)
				if err != nil {
					return err
				}
				before = &t
			}

			chatJIDs, err := messageChatJIDFilter(ctx, a, chat)
			if err != nil {
				return err
			}

			msgs, err := a.DB().SearchMessages(store.SearchMessagesParams{
				Query:     args[0],
				ChatJIDs:  chatJIDs,
				From:      from,
				Limit:     limit,
				After:     after,
				Before:    before,
				HasMedia:  hasMedia,
				Type:      msgType,
				Forwarded: forwarded,
				Starred:   starred,
			})
			if err != nil {
				return err
			}
			msgs = resolveMessageSenderNames(ctx, a, msgs)

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{
					"messages": msgs,
					"fts":      a.DB().HasFTS(),
				})
			}

			if err := writeMessagesSearch(os.Stdout, msgs, fullTableOutput(flags.fullOutput)); err != nil {
				return err
			}
			if !a.DB().HasFTS() {
				fmt.Fprintln(os.Stderr, "Note: FTS5 not enabled; search is using LIKE (slow).")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&chat, "chat", "", "chat JID")
	cmd.Flags().StringVar(&from, "from", "", "sender JID")
	cmd.Flags().IntVar(&limit, "limit", 50, "limit results")
	cmd.Flags().StringVar(&afterStr, "after", "", "only messages after time (RFC3339 or YYYY-MM-DD)")
	cmd.Flags().StringVar(&beforeStr, "before", "", "only messages before time (RFC3339 or YYYY-MM-DD)")
	cmd.Flags().BoolVar(&hasMedia, "has-media", false, "only messages with media")
	cmd.Flags().StringVar(&msgType, "type", "", "message type filter (text|image|video|audio|document)")
	cmd.Flags().BoolVar(&forwarded, "forwarded", false, "only forwarded messages")
	cmd.Flags().BoolVar(&starred, "starred", false, "only starred messages")
	return cmd
}

func newMessagesStarredCmd(flags *rootFlags) *cobra.Command {
	var chat string
	var limit int
	var afterStr string
	var beforeStr string
	var asc bool

	cmd := &cobra.Command{
		Use:   "starred",
		Short: "List starred messages",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, false, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			var after *time.Time
			var before *time.Time
			if afterStr != "" {
				t, err := parseTime(afterStr)
				if err != nil {
					return err
				}
				after = &t
			}
			if beforeStr != "" {
				t, err := parseTime(beforeStr)
				if err != nil {
					return err
				}
				before = &t
			}

			chatJIDs, err := messageChatJIDFilter(ctx, a, chat)
			if err != nil {
				return err
			}
			msgs, err := a.DB().ListStarredMessages(store.ListStarredMessagesParams{
				ChatJIDs: chatJIDs,
				Limit:    limit,
				After:    after,
				Before:   before,
				Asc:      asc,
			})
			if err != nil {
				return err
			}
			msgs = resolveMessageSenderNames(ctx, a, msgs)

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{
					"messages": msgs,
					"fts":      a.DB().HasFTS(),
				})
			}
			return writeMessagesStarred(os.Stdout, msgs, fullTableOutput(flags.fullOutput))
		},
	}
	cmd.Flags().StringVar(&chat, "chat", "", "filter by chat JID")
	cmd.Flags().IntVar(&limit, "limit", 50, "max number of messages to return")
	cmd.Flags().StringVar(&afterStr, "after", "", "only messages with stored star time after time (RFC3339 or YYYY-MM-DD)")
	cmd.Flags().StringVar(&beforeStr, "before", "", "only messages with stored star time before time (RFC3339 or YYYY-MM-DD)")
	cmd.Flags().BoolVar(&asc, "asc", false, "show oldest starred messages first (default: newest starred first)")
	return cmd
}

func newMessagesShowCmd(flags *rootFlags) *cobra.Command {
	var chat string
	var id string

	cmd := &cobra.Command{
		Use:   "show",
		Short: "Show one message",
		RunE: func(cmd *cobra.Command, args []string) error {
			if chat == "" || id == "" {
				return fmt.Errorf("--chat and --id are required")
			}

			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, false, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			chatJIDs, err := messageChatJIDFilter(ctx, a, chat)
			if err != nil {
				return err
			}
			m, err := getMessageByChatFilter(a.DB(), chatJIDs, id)
			if err != nil {
				return err
			}
			m = resolveMessageSenderNames(ctx, a, []store.Message{m})[0]

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, m)
			}

			return writeMessageShow(os.Stdout, m)
		},
	}

	cmd.Flags().StringVar(&chat, "chat", "", "chat JID")
	cmd.Flags().StringVar(&id, "id", "", "message ID")
	return cmd
}

func newMessagesContextCmd(flags *rootFlags) *cobra.Command {
	var chat string
	var id string
	var before int
	var after int

	cmd := &cobra.Command{
		Use:   "context",
		Short: "Show message context around a message ID",
		RunE: func(cmd *cobra.Command, args []string) error {
			if chat == "" || id == "" {
				return fmt.Errorf("--chat and --id are required")
			}

			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, false, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			chatJIDs, err := messageChatJIDFilter(ctx, a, chat)
			if err != nil {
				return err
			}
			msgs, err := getMessageContextByChatFilter(a.DB(), chatJIDs, id, before, after)
			if err != nil {
				return err
			}
			msgs = resolveMessageSenderNames(ctx, a, msgs)

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, msgs)
			}

			return writeMessageContext(os.Stdout, msgs, id, fullTableOutput(flags.fullOutput))
		},
	}
	cmd.Flags().StringVar(&chat, "chat", "", "chat JID")
	cmd.Flags().StringVar(&id, "id", "", "message ID")
	cmd.Flags().IntVar(&before, "before", 5, "messages before")
	cmd.Flags().IntVar(&after, "after", 5, "messages after")
	return cmd
}

func newMessagesExportCmd(flags *rootFlags) *cobra.Command {
	var chat string
	var limit int
	var afterStr string
	var beforeStr string
	var output string

	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export messages as JSON",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, false, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			var after *time.Time
			var before *time.Time
			if afterStr != "" {
				t, err := parseTime(afterStr)
				if err != nil {
					return err
				}
				after = &t
			}
			if beforeStr != "" {
				t, err := parseTime(beforeStr)
				if err != nil {
					return err
				}
				before = &t
			}

			chatJIDs, err := messageChatJIDFilter(ctx, a, chat)
			if err != nil {
				return err
			}

			msgs, err := a.DB().ListMessages(store.ListMessagesParams{
				ChatJIDs: chatJIDs,
				Limit:    limit,
				After:    after,
				Before:   before,
				Asc:      true,
			})
			if err != nil {
				return err
			}
			msgs = resolveMessageSenderNames(ctx, a, msgs)

			dst := os.Stdout
			if output != "" {
				f, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
				if err != nil {
					return err
				}
				defer f.Close()
				dst = f
			}

			return out.WriteJSON(dst, map[string]any{
				"messages": msgs,
				"fts":      a.DB().HasFTS(),
			})
		},
	}

	cmd.Flags().StringVar(&chat, "chat", "", "filter by chat JID")
	cmd.Flags().IntVar(&limit, "limit", 1000, "max number of messages to export")
	cmd.Flags().StringVar(&afterStr, "after", "", "only messages after time (RFC3339 or YYYY-MM-DD)")
	cmd.Flags().StringVar(&beforeStr, "before", "", "only messages before time (RFC3339 or YYYY-MM-DD)")
	cmd.Flags().StringVar(&output, "output", "", "write JSON export to file instead of stdout")
	return cmd
}

func newMessagesDeleteCmd(flags *rootFlags) *cobra.Command {
	var chat string
	var id string
	var forMe bool
	var deleteMedia bool
	postSendWait := postSendRetryReceiptWait

	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete a message for everyone or for you",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(chat) == "" || strings.TrimSpace(id) == "" {
				return fmt.Errorf("--chat and --id are required")
			}
			if deleteMedia && !forMe {
				return fmt.Errorf("--delete-media requires --for-me")
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
			msg, chatJID, err := loadMessageMutationTarget(ctx, a, chat, id)
			if err != nil {
				return err
			}
			if !forMe {
				if err := validateMessageCanRevoke(msg); err != nil {
					return err
				}
			} else if err := validateMessageCanDeleteForMe(msg); err != nil {
				return err
			}
			if err := a.Connect(ctx, false, nil); err != nil {
				return err
			}
			if err := warnRapidSendIfNeeded(a.StoreDir(), time.Now().UTC(), os.Stderr); err != nil {
				return err
			}
			if forMe {
				info, err := messageInfoForDeleteForMe(msg, chatJID)
				if err != nil {
					return err
				}
				if _, err := runSendOperation(ctx, reconnectForSend(a), func(ctx context.Context) (struct{}, error) {
					return struct{}{}, a.WA().DeleteMessageForMe(ctx, info, deleteMedia)
				}); err != nil {
					return err
				}
				if err := a.DB().MarkMessageDeletedForMe(msg.ChatJID, msg.MsgID, msg.SenderJID, msg.FromMe, time.Now().UTC()); err != nil {
					return fmt.Errorf("store deleted-for-me message state: %w", err)
				}

				waitForPostSendRetryReceipts(ctx, postSendWait)

				if flags.asJSON {
					return out.WriteJSON(os.Stdout, map[string]any{
						"deleted_for_me": true,
						"to":             chatJID.String(),
						"target":         msg.MsgID,
					})
				}
				fmt.Fprintf(os.Stdout, "Deleted message %s for me in %s\n", msg.MsgID, chatJID.String())
				return nil
			}
			sentID, err := runSendOperation(ctx, reconnectForSend(a), func(ctx context.Context) (types.MessageID, error) {
				return a.WA().RevokeMessage(ctx, chatJID, types.MessageID(msg.MsgID))
			})
			if err != nil {
				return err
			}
			if err := a.DB().MarkMessageRevoked(msg.ChatJID, msg.MsgID); err != nil {
				return fmt.Errorf("store deleted message state: %w", err)
			}

			waitForPostSendRetryReceipts(ctx, postSendWait)

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{
					"revoked": true,
					"to":      chatJID.String(),
					"id":      sentID,
					"target":  msg.MsgID,
				})
			}
			fmt.Fprintf(os.Stdout, "Deleted message %s in %s (id %s)\n", msg.MsgID, chatJID.String(), sentID)
			return nil
		},
	}
	cmd.Flags().StringVar(&chat, "chat", "", "chat JID, phone number, or contact/group/chat name")
	cmd.Flags().StringVar(&id, "id", "", "message ID to delete")
	cmd.Flags().BoolVar(&forMe, "for-me", false, "delete the message only for this WhatsApp account")
	cmd.Flags().BoolVar(&deleteMedia, "delete-media", false, "also remove local media when used with --for-me")
	cmd.Flags().DurationVar(&postSendWait, "post-send-wait", postSendRetryReceiptWait, "keep the connection alive after delete so retry receipts can be handled (0 disables)")
	return cmd
}

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
				return err
			}
			defer closeApp(a, lk)

			if err := a.EnsureAuthed(); err != nil {
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

func loadMessageMutationTarget(ctx context.Context, a *app.App, chat, id string) (store.Message, types.JID, error) {
	chatJIDs, err := messageChatJIDFilter(ctx, a, chat)
	if err != nil {
		return store.Message{}, types.JID{}, err
	}
	msg, err := getMessageByChatFilter(a.DB(), chatJIDs, id)
	if err != nil {
		return store.Message{}, types.JID{}, err
	}
	chatJID, err := wa.ParseUserOrJID(msg.ChatJID)
	if err != nil {
		return store.Message{}, types.JID{}, fmt.Errorf("stored chat JID is invalid: %w", err)
	}
	return msg, chatJID, nil
}

func validateMessageCanRevoke(msg store.Message) error {
	if msg.Revoked {
		return fmt.Errorf("message %s is already deleted", msg.MsgID)
	}
	if msg.DeletedForMe {
		return fmt.Errorf("message %s was deleted for me", msg.MsgID)
	}
	if !msg.FromMe {
		return fmt.Errorf("message %s was not sent by me", msg.MsgID)
	}
	return nil
}

func validateMessageCanDeleteForMe(msg store.Message) error {
	if msg.Revoked {
		return fmt.Errorf("message %s is already deleted", msg.MsgID)
	}
	if msg.DeletedForMe {
		return fmt.Errorf("message %s was deleted for me", msg.MsgID)
	}
	return nil
}

func messageInfoForDeleteForMe(msg store.Message, chat types.JID) (types.MessageInfo, error) {
	sender := types.EmptyJID
	if strings.TrimSpace(msg.SenderJID) != "" {
		parsed, err := types.ParseJID(msg.SenderJID)
		if err != nil {
			return types.MessageInfo{}, fmt.Errorf("stored sender JID is invalid: %w", err)
		}
		sender = parsed
	} else if !msg.FromMe && chat.Server == types.DefaultUserServer {
		sender = chat
	}
	if !msg.FromMe && chat.Server == types.GroupServer && sender.IsEmpty() {
		return types.MessageInfo{}, fmt.Errorf("stored sender JID is required to delete a group message for me")
	}
	return types.MessageInfo{
		MessageSource: types.MessageSource{
			Chat:     chat,
			Sender:   sender,
			IsFromMe: msg.FromMe,
			IsGroup:  chat.Server == types.GroupServer,
		},
		ID:        types.MessageID(msg.MsgID),
		Timestamp: msg.Timestamp,
	}, nil
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

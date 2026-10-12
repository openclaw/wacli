package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/store"
	"github.com/spf13/cobra"
)

func newMessagesListCmd(flags *rootFlags) *cobra.Command {
	var chat string
	var sender string
	var limit int
	var cursor string
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

			after, before, err := messageTimeBounds(afterStr, beforeStr)
			if err != nil {
				return err
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

			sender, err = localMessageIdentifier(a.DB(), sender, false)
			if err != nil {
				return err
			}
			chatJIDs, err := messageChatJIDFilter(ctx, a, chat)
			if err != nil {
				return err
			}

			page, err := readListMessagePage(ctx, a, cmd.Name(), store.ListMessagesParams{
				ChatJIDs:  chatJIDs,
				SenderJID: sender,
				Limit:     limit,
				After:     after,
				Before:    before,
				FromMe:    fromMeFilter,
				Asc:       asc,
				Forwarded: forwarded,
				Starred:   starred,
			}, cursor)
			if err != nil {
				return err
			}
			msgs := page.Messages
			messagePageHint(page)

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, page)
			}

			return writeMessagesList(os.Stdout, msgs, fullTableOutput(flags.fullOutput))
		},
	}

	cmd.Flags().StringVar(&chat, "chat", "", "filter by chat number, JID, or exact local name")
	cmd.Flags().StringVar(&sender, "sender", "", "filter by sender number, JID, or exact local contact name")
	cmd.Flags().StringVar(&cursor, "cursor", "", "continue an unchanged local archive read; repeat original filters")
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
	var cursor string
	var afterStr string
	var beforeStr string
	var hasMedia bool
	var msgType string
	var forwarded bool
	var starred bool
	var contextBefore, contextAfter int

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

			after, before, err := messageTimeBounds(afterStr, beforeStr)
			if err != nil {
				return err
			}

			from, err = localMessageIdentifier(a.DB(), from, false)
			if err != nil {
				return err
			}
			chatJIDs, err := messageChatJIDFilter(ctx, a, chat)
			if err != nil {
				return err
			}

			page, contexts, err := readSearchMessagePage(ctx, a, store.SearchMessagesParams{
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
			}, cursor, contextBefore, contextAfter)
			if err != nil {
				return err
			}
			msgs := page.Messages
			messagePageHint(page)

			if flags.asJSON {
				data := map[string]any{"messages": page.Messages, "fts": page.FTS, "page": page.Page, "source": page.Source}
				if contextBefore > 0 || contextAfter > 0 {
					data["context"] = contexts
				}
				return out.WriteJSON(os.Stdout, data)
			}

			if err := writeMessagesSearch(os.Stdout, msgs, fullTableOutput(flags.fullOutput)); err != nil {
				return err
			}
			for _, c := range contexts {
				fmt.Fprintf(os.Stdout, "\nContext for %s in %s:\n", sanitize(c.MsgID), sanitize(c.ChatJID))
				if err := writeMessageContext(os.Stdout, c.Messages, c.MsgID, fullTableOutput(flags.fullOutput)); err != nil {
					return err
				}
			}
			if !a.DB().HasFTS() {
				fmt.Fprintln(os.Stderr, "Note: FTS5 not enabled; search is using LIKE (slow).")
			}
			return nil
		},
	}

	cmd.Flags().IntVar(&contextBefore, "context-before", 0, "visible neighbors before each hit (0..20; at most 100 hits)")
	cmd.Flags().IntVar(&contextAfter, "context-after", 0, "visible neighbors after each hit (0..20; at most 100 hits)")
	cmd.Flags().StringVar(&chat, "chat", "", "chat number, JID, or exact local name")
	cmd.Flags().StringVar(&from, "from", "", "sender number, JID, or exact local contact name")
	cmd.Flags().StringVar(&cursor, "cursor", "", "continue an unchanged local archive read; repeat original filters")
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

			after, before, err := messageTimeBounds(afterStr, beforeStr)
			if err != nil {
				return err
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
	cmd.Flags().StringVar(&chat, "chat", "", "filter by chat number, JID, or exact local name")
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

	cmd.Flags().StringVar(&chat, "chat", "", "chat number, JID, or exact local name")
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
	cmd.Flags().StringVar(&chat, "chat", "", "chat number, JID, or exact local name")
	cmd.Flags().StringVar(&id, "id", "", "message ID")
	cmd.Flags().IntVar(&before, "before", 5, "messages before")
	cmd.Flags().IntVar(&after, "after", 5, "messages after")
	return cmd
}

func newMessagesExportCmd(flags *rootFlags) *cobra.Command {
	var chat string
	var limit int
	var cursor string
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

			after, before, err := messageTimeBounds(afterStr, beforeStr)
			if err != nil {
				return err
			}

			chatJIDs, err := messageChatJIDFilter(ctx, a, chat)
			if err != nil {
				return err
			}

			page, err := readListMessagePage(ctx, a, cmd.Name(), store.ListMessagesParams{
				ChatJIDs: chatJIDs,
				Limit:    limit,
				After:    after,
				Before:   before,
				Asc:      true,
			}, cursor)
			if err != nil {
				return err
			}
			messagePageHint(page)

			dst := os.Stdout
			if output != "" {
				f, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
				if err != nil {
					return err
				}
				defer f.Close()
				dst = f
			}

			return out.WriteJSON(dst, page)
		},
	}

	cmd.Flags().StringVar(&chat, "chat", "", "filter by chat number, JID, or exact local name")
	cmd.Flags().StringVar(&cursor, "cursor", "", "continue an unchanged local archive read; repeat original filters")
	cmd.Flags().IntVar(&limit, "limit", 1000, "max number of messages to export")
	cmd.Flags().StringVar(&afterStr, "after", "", "only messages after time (RFC3339 or YYYY-MM-DD)")
	cmd.Flags().StringVar(&beforeStr, "before", "", "only messages before time (RFC3339 or YYYY-MM-DD)")
	cmd.Flags().StringVar(&output, "output", "", "write JSON export to file instead of stdout")
	return cmd
}

func messageTimeBounds(afterStr, beforeStr string) (after, before *time.Time, err error) {
	if afterStr != "" {
		t, err := parseTime(afterStr)
		if err != nil {
			return nil, nil, err
		}
		after = &t
	}
	if beforeStr != "" {
		t, err := parseTime(beforeStr)
		if err != nil {
			return nil, nil, err
		}
		before = &t
	}
	return after, before, nil
}

func messagePageHint(page store.MessagePage) {
	if page.Page.HasMore {
		if page.Page.NextCursor != nil {
			fmt.Fprintf(os.Stderr, "More local messages: repeat this command with --cursor %s\n", *page.Page.NextCursor)
		} else {
			fmt.Fprintln(os.Stderr, "More local messages; cursor support requires a writable store migration.")
		}
	}
}

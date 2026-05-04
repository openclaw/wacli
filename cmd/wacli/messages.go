package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steipete/wacli/internal/app"
	"github.com/steipete/wacli/internal/out"
	"github.com/steipete/wacli/internal/store"
	"github.com/steipete/wacli/internal/wa"
	"go.mau.fi/whatsmeow/types"
)

func newMessagesCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "messages",
		Short: "List and search messages from the local DB",
	}
	cmd.AddCommand(newMessagesListCmd(flags))
	cmd.AddCommand(newMessagesSearchCmd(flags))
	cmd.AddCommand(newMessagesShowCmd(flags))
	cmd.AddCommand(newMessagesContextCmd(flags))
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
	return cmd
}

func messageChatJIDFilter(ctx context.Context, a *app.App, chat string) ([]string, error) {
	chat = strings.TrimSpace(chat)
	if chat == "" {
		return nil, nil
	}
	jid, err := wa.ParseUserOrJID(chat)
	if err != nil {
		return nil, err
	}
	jids := []types.JID{canonicalMessageFilterJID(jid)}
	if _, err := os.Stat(filepath.Join(a.StoreDir(), "session.db")); err != nil {
		return jidStrings(jids), nil
	}
	if err := a.OpenWA(); err != nil {
		return jidStrings(jids), nil
	}
	client := a.WA()
	if client == nil {
		return jidStrings(jids), nil
	}
	switch jid.Server {
	case types.DefaultUserServer:
		jids = append(jids, canonicalMessageFilterJID(client.ResolvePNToLID(ctx, jid)))
	case types.HiddenUserServer:
		jids = append(jids, canonicalMessageFilterJID(client.ResolveLIDToPN(ctx, jid)))
	}
	return jidStrings(jids), nil
}

func canonicalMessageFilterJID(jid types.JID) types.JID {
	if jid.Server == types.DefaultUserServer {
		return jid.ToNonAD()
	}
	return jid
}

func jidStrings(jids []types.JID) []string {
	out := make([]string, 0, len(jids))
	seen := make(map[string]struct{}, len(jids))
	for _, jid := range jids {
		if jid.IsEmpty() {
			continue
		}
		s := jid.String()
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

type lidSenderResolver interface {
	ResolveLIDToPN(context.Context, types.JID) types.JID
}

func resolveMessageSenderNames(ctx context.Context, a *app.App, msgs []store.Message) []store.Message {
	if len(msgs) == 0 || !messagesNeedSenderResolution(msgs) {
		return msgs
	}
	if _, err := os.Stat(filepath.Join(a.StoreDir(), "session.db")); err != nil {
		return msgs
	}
	if err := a.OpenWA(); err != nil {
		return msgs
	}
	return resolveMessageSenderNamesWith(ctx, a.DB(), a.WA(), msgs)
}

func messagesNeedSenderResolution(msgs []store.Message) bool {
	for _, msg := range msgs {
		if !msg.FromMe && strings.TrimSpace(msg.SenderName) == "" && strings.HasSuffix(strings.TrimSpace(msg.SenderJID), "@"+types.HiddenUserServer) {
			return true
		}
	}
	return false
}

func resolveMessageSenderNamesWith(ctx context.Context, db *store.DB, resolver lidSenderResolver, msgs []store.Message) []store.Message {
	if resolver == nil {
		return msgs
	}
	cache := map[string]string{}
	for i := range msgs {
		if msgs[i].FromMe || strings.TrimSpace(msgs[i].SenderName) != "" {
			continue
		}
		sender := strings.TrimSpace(msgs[i].SenderJID)
		if sender == "" {
			continue
		}
		if name, ok := cache[sender]; ok {
			msgs[i].SenderName = name
			continue
		}
		name := resolvedSenderName(ctx, db, resolver, sender)
		cache[sender] = name
		msgs[i].SenderName = name
	}
	return msgs
}

func resolvedSenderName(ctx context.Context, db *store.DB, resolver lidSenderResolver, sender string) string {
	jid, err := types.ParseJID(sender)
	if err != nil || jid.Server != types.HiddenUserServer {
		return ""
	}
	pn := resolver.ResolveLIDToPN(ctx, jid)
	if pn.IsEmpty() || pn == jid {
		return ""
	}
	contact, err := db.GetContact(pn.String())
	if err == nil {
		if contact.Alias != "" {
			return contact.Alias
		}
		if contact.Name != "" {
			return contact.Name
		}
		if contact.Phone != "" {
			return contact.Phone
		}
	}
	return pn.String()
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

func getMessageByChatFilter(db *store.DB, chatJIDs []string, id string) (store.Message, error) {
	var notFound error
	for _, chatJID := range chatJIDs {
		m, err := db.GetMessage(chatJID, id)
		if err == nil {
			return m, nil
		}
		if !isNoRows(err) {
			return store.Message{}, err
		}
		notFound = err
	}
	if notFound != nil {
		return store.Message{}, notFound
	}
	return store.Message{}, sql.ErrNoRows
}

func getMessageContextByChatFilter(db *store.DB, chatJIDs []string, id string, before, after int) ([]store.Message, error) {
	var notFound error
	for _, chatJID := range chatJIDs {
		msgs, err := db.MessageContext(chatJID, id, before, after)
		if err == nil {
			return msgs, nil
		}
		if !isNoRows(err) {
			return nil, err
		}
		notFound = err
	}
	if notFound != nil {
		return nil, notFound
	}
	return nil, sql.ErrNoRows
}

func isNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}

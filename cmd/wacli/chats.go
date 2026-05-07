package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/store"
	"github.com/spf13/cobra"
	"go.mau.fi/whatsmeow/types"
)

func newChatsCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "chats",
		Short: "List and manage chats",
	}
	cmd.AddCommand(newChatsListCmd(flags))
	cmd.AddCommand(newChatsShowCmd(flags))
	cmd.AddCommand(newChatsArchiveCmd(flags, true))
	cmd.AddCommand(newChatsArchiveCmd(flags, false))
	cmd.AddCommand(newChatsPinCmd(flags, true))
	cmd.AddCommand(newChatsPinCmd(flags, false))
	cmd.AddCommand(newChatsMuteCmd(flags))
	cmd.AddCommand(newChatsUnmuteCmd(flags))
	cmd.AddCommand(newChatsMarkReadCmd(flags, true))
	cmd.AddCommand(newChatsMarkReadCmd(flags, false))
	cmd.AddCommand(newChatsCleanupCmd(flags))
	return cmd
}

func newChatsListCmd(flags *rootFlags) *cobra.Command {
	var query string
	var limit int
	var archived, noArchived bool
	var pinned, noPinned bool
	var muted, noMuted bool
	var unread, noUnread bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List chats",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateBoolFilter("archived", archived, noArchived); err != nil {
				return err
			}
			if err := validateBoolFilter("pinned", pinned, noPinned); err != nil {
				return err
			}
			if err := validateBoolFilter("muted", muted, noMuted); err != nil {
				return err
			}
			if err := validateBoolFilter("unread", unread, noUnread); err != nil {
				return err
			}

			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, false, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			filter := store.ChatListFilter{
				Query:    query,
				Limit:    limit,
				Archived: boolFilter(archived, noArchived),
				Pinned:   boolFilter(pinned, noPinned),
				Muted:    boolFilter(muted, noMuted),
				Unread:   boolFilter(unread, noUnread),
			}
			chats, err := a.DB().ListChatsFiltered(filter)
			if err != nil {
				return err
			}
			chats = resolveStoredChats(ctx, a, chats)
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, chats)
			}

			fullOutput := fullTableOutput(flags.fullOutput)
			w := newTableWriter(os.Stdout)
			fmt.Fprintln(w, "KIND\tNAME\tJID\tLAST\tFLAGS")
			for _, c := range chats {
				name := c.Name
				if name == "" {
					name = c.JID
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", c.Kind, tableCell(name, 28, fullOutput), c.JID, c.LastMessageTS.Local().Format("2006-01-02 15:04:05"), chatFlagsString(c))
			}
			_ = w.Flush()
			return nil
		},
	}
	cmd.Flags().StringVar(&query, "query", "", "search query")
	cmd.Flags().IntVar(&limit, "limit", 50, "limit")
	cmd.Flags().BoolVar(&archived, "archived", false, "show only archived chats")
	cmd.Flags().BoolVar(&noArchived, "no-archived", false, "exclude archived chats")
	cmd.Flags().BoolVar(&pinned, "pinned", false, "show only pinned chats")
	cmd.Flags().BoolVar(&noPinned, "no-pinned", false, "exclude pinned chats")
	cmd.Flags().BoolVar(&muted, "muted", false, "show only muted chats")
	cmd.Flags().BoolVar(&noMuted, "no-muted", false, "exclude muted chats")
	cmd.Flags().BoolVar(&unread, "unread", false, "show only unread chats")
	cmd.Flags().BoolVar(&noUnread, "no-unread", false, "exclude unread chats")
	return cmd
}

func newChatsShowCmd(flags *rootFlags) *cobra.Command {
	var jid string
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Show one chat",
		RunE: func(cmd *cobra.Command, args []string) error {
			if jid == "" {
				return fmt.Errorf("--jid is required")
			}
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, false, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			c, err := getChatForDisplay(ctx, a, jid)
			if err != nil {
				return err
			}
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, c)
			}
			fmt.Fprintf(os.Stdout, "JID: %s\nKind: %s\nName: %s\nLast: %s\nArchived: %t\nPinned: %t\nMuted: %t\nMuted until: %s\nUnread: %t\n",
				c.JID, c.Kind, c.Name, c.LastMessageTS.Local().Format(time.RFC3339), c.Archived, c.Pinned, c.Muted(), formatMutedUntil(c.MutedUntil), c.Unread)
			return nil
		},
	}
	cmd.Flags().StringVar(&jid, "jid", "", "chat JID")
	return cmd
}

type chatDisplayResolver interface {
	ResolveChatName(context.Context, types.JID, string) string
	ResolveLIDToPN(context.Context, types.JID) types.JID
	ResolvePNToLID(context.Context, types.JID) types.JID
}

func resolveStoredChats(ctx context.Context, a *app.App, chats []store.Chat) []store.Chat {
	if len(chats) == 0 || !chatsNeedLIDResolution(chats) {
		return chats
	}
	if _, err := os.Stat(filepath.Join(a.StoreDir(), "session.db")); err != nil {
		return chats
	}
	if err := a.OpenWA(); err != nil {
		return chats
	}
	return resolveStoredChatsWith(ctx, a.WA(), chats)
}

func chatsNeedLIDResolution(chats []store.Chat) bool {
	for _, chat := range chats {
		if strings.HasSuffix(strings.TrimSpace(chat.JID), "@"+types.HiddenUserServer) {
			return true
		}
	}
	return false
}

func resolveStoredChatsWith(ctx context.Context, resolver chatDisplayResolver, chats []store.Chat) []store.Chat {
	out := make([]store.Chat, 0, len(chats))
	seen := make(map[string]int, len(chats))
	for _, chat := range chats {
		chat = resolveStoredChatWith(ctx, resolver, chat)
		if idx, ok := seen[chat.JID]; ok {
			out[idx] = mergeDisplayChats(out[idx], chat)
			continue
		}
		seen[chat.JID] = len(out)
		out = append(out, chat)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].LastMessageTS.After(out[j].LastMessageTS)
	})
	return out
}

func resolveStoredChatWith(ctx context.Context, resolver chatDisplayResolver, chat store.Chat) store.Chat {
	jid, err := types.ParseJID(strings.TrimSpace(chat.JID))
	if err != nil || jid.Server != types.HiddenUserServer {
		return chat
	}
	pn := resolver.ResolveLIDToPN(ctx, jid)
	if pn.IsEmpty() || pn.Server != types.DefaultUserServer {
		return chat
	}

	out := chat
	out.JID = pn.ToNonAD().String()
	if out.Kind == "" || out.Kind == "unknown" {
		out.Kind = "dm"
	}
	if chatNameRank(out.Name, chat.JID) < 2 {
		if name := strings.TrimSpace(resolver.ResolveChatName(ctx, pn, "")); name != "" {
			out.Name = name
		}
	}
	if strings.TrimSpace(out.Name) == "" || strings.TrimSpace(out.Name) == strings.TrimSpace(chat.JID) {
		out.Name = out.JID
	}
	return out
}

func mergeDisplayChats(a, b store.Chat) store.Chat {
	out := a
	if b.LastMessageTS.After(out.LastMessageTS) {
		out.LastMessageTS = b.LastMessageTS
	}
	if out.Kind == "" || out.Kind == "unknown" || b.Kind == "dm" {
		out.Kind = b.Kind
	}
	if chatNameRank(b.Name, b.JID) > chatNameRank(out.Name, out.JID) {
		out.Name = b.Name
	}
	return out
}

func chatNameRank(name, jid string) int {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return 0
	case name == strings.TrimSpace(jid), strings.Contains(name, "@"):
		return 1
	default:
		return 2
	}
}

func getChatForDisplay(ctx context.Context, a *app.App, rawJID string) (store.Chat, error) {
	chat, err := a.DB().GetChat(rawJID)
	if err == nil {
		return resolveStoredChatForDisplay(ctx, a, chat), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return store.Chat{}, err
	}

	chatJIDs := mappedChatJIDs(ctx, a, rawJID)
	for _, chatJID := range chatJIDs {
		if chatJID == rawJID {
			continue
		}
		chat, err = a.DB().GetChat(chatJID)
		if err == nil {
			return resolveStoredChatForDisplay(ctx, a, chat), nil
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return store.Chat{}, err
		}
	}
	return store.Chat{}, sql.ErrNoRows
}

func resolveStoredChatForDisplay(ctx context.Context, a *app.App, chat store.Chat) store.Chat {
	return resolveStoredChats(ctx, a, []store.Chat{chat})[0]
}

func mappedChatJIDs(ctx context.Context, a *app.App, rawJID string) []string {
	jid, err := types.ParseJID(strings.TrimSpace(rawJID))
	if err != nil {
		return []string{rawJID}
	}
	jids := []types.JID{jid}
	if _, err := os.Stat(filepath.Join(a.StoreDir(), "session.db")); err != nil {
		return jidStrings(jids)
	}
	if err := a.OpenWA(); err != nil {
		return jidStrings(jids)
	}
	client := a.WA()
	if client == nil {
		return jidStrings(jids)
	}
	switch jid.Server {
	case types.DefaultUserServer:
		jids = append(jids, client.ResolvePNToLID(ctx, jid))
	case types.HiddenUserServer:
		jids = append(jids, client.ResolveLIDToPN(ctx, jid))
	}
	return jidStrings(jids)
}

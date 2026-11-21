package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow/types"
)

func messageChatJIDFilter(ctx context.Context, a *app.App, chat string) ([]string, error) {
	chat = strings.TrimSpace(chat)
	if chat == "" {
		return nil, nil
	}
	resolved, err := localMessageIdentifier(a.DB(), chat, true)
	if err != nil {
		return nil, err
	}
	jid, err := wa.ParseUserOrJID(resolved)
	if err != nil {
		return nil, err
	}
	jids := []types.JID{canonicalMessageFilterJID(jid)}
	if _, err := os.Stat(filepath.Join(a.StoreDir(), "session.db")); err != nil {
		return jidStrings(jids), nil
	}
	resolver, err := a.LocalResolver()
	if err != nil {
		return jidStrings(jids), nil
	}
	switch jid.Server {
	case types.DefaultUserServer:
		jids = append(jids, canonicalMessageFilterJID(resolver.ResolvePNToLID(ctx, jid)))
	case types.HiddenUserServer:
		jids = append(jids, canonicalMessageFilterJID(resolver.ResolveLIDToPN(ctx, jid)))
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
	return resolveMessageSenderNamesInView(ctx, a, a.DB(), msgs)
}

func resolveMessageSenderNamesInView(ctx context.Context, a *app.App, db *store.DB, msgs []store.Message) []store.Message {
	return withQuotes(ctx, a, db, resolveSenderNamesInView(ctx, a, db, msgs))
}

// withQuotes fills each reply's quote, for every view and --json alike: sync
// stored the quote without its author, and a media message as its placeholder
// alone ("> Sent image"). Who wrote the quoted message and what it says come
// from the original in the store ("me" for the account's own; a media
// message's caption, a document's file name); without it, the quote sync
// stored and the quoted sender's name (a contact's, else the number).
func withQuotes(ctx context.Context, a *app.App, db *store.DB, msgs []store.Message) []store.Message {
	names := map[string]string{}
	name := func(jid string) string {
		jid = strings.TrimSpace(jid)
		if n, ok := names[jid]; ok || jid == "" {
			return n
		}
		n := resolveSenderNamesInView(ctx, a, db, []store.Message{{SenderJID: jid}})[0].SenderName
		if c, err := db.GetContact(jid); n == "" && err == nil {
			if n = strings.TrimSpace(c.Alias); n == "" {
				n = strings.TrimSpace(c.Name)
			}
		}
		if n == "" {
			n, _, _ = strings.Cut(jid, "@")
		}
		names[jid] = n
		return n
	}
	for i := range msgs {
		m := &msgs[i]
		if strings.TrimSpace(m.QuotedMsgID) == "" {
			continue
		}
		text, who := "", name(m.QuotedSenderJID)
		if q, err := db.GetMessage(m.ChatJID, m.QuotedMsgID); err == nil {
			if text = ownLine(q); text == "(message)" {
				text = ""
			}
			switch {
			case q.FromMe:
				who = "me"
			case strings.TrimSpace(q.SenderName) != "":
				who = strings.TrimSpace(q.SenderName)
			case strings.TrimSpace(q.SenderJID) != "":
				who = name(q.SenderJID)
			}
		}
		if text == "" {
			text, _, _ = replyParts(*m)
		}
		if text != "" {
			m.QuotedText, m.QuotedSenderName = text, who
		}
	}
	return msgs
}

func resolveSenderNamesInView(ctx context.Context, a *app.App, db *store.DB, msgs []store.Message) []store.Message {
	if len(msgs) == 0 || !messagesNeedSenderResolution(msgs) {
		return msgs
	}
	if _, err := os.Stat(filepath.Join(a.StoreDir(), "session.db")); err != nil {
		return msgs
	}
	resolver, err := a.LocalResolver()
	if err != nil {
		return msgs
	}
	return resolveMessageSenderNamesWith(ctx, db, resolver, msgs)
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

func getMessageByChatFilter(db *store.DB, chatJIDs []string, id string) (store.Message, error) {
	var notFound error
	for _, chatJID := range chatJIDs {
		m, err := db.GetMessage(chatJID, id)
		if err == nil {
			// GetMessage reads its own columns, so the delivery state has to be
			// filled in here for `show` to agree with `list`.
			if counts, cerr := db.MessageReceipts(m.ChatJID, m.MsgID); cerr == nil {
				m.DeliveredTo = counts.Delivered
				m.ReadBy = counts.Read
			}
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

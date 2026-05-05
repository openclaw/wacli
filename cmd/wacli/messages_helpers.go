package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/steipete/wacli/internal/app"
	"github.com/steipete/wacli/internal/store"
	"github.com/steipete/wacli/internal/wa"
	"go.mau.fi/whatsmeow/types"
)

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

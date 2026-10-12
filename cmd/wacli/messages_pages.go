package main

import (
	"context"
	"fmt"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/wa"
)

type searchHitContext struct {
	ChatJID  string          `json:"chat_jid"`
	MsgID    string          `json:"msg_id"`
	Messages []store.Message `json:"messages"`
}

func readListMessagePage(ctx context.Context, a *app.App, command string, p store.ListMessagesParams, cursor string) (store.MessagePage, error) {
	var page store.MessagePage
	err := a.DB().ReadView(func(db *store.DB) error {
		var err error
		page, err = db.ListMessagesPage(command, p, cursor)
		if err != nil {
			return err
		}
		page.Messages = resolveMessageSenderNamesInView(ctx, a, db, page.Messages)
		return nil
	})
	return page, err
}
func readSearchMessagePage(ctx context.Context, a *app.App, p store.SearchMessagesParams, cursor string, before, after int) (store.MessagePage, []searchHitContext, error) {
	var page store.MessagePage
	var contexts []searchHitContext
	if before < 0 || after < 0 || before > 20 || after > 20 {
		return page, nil, fmt.Errorf("context counts must be between 0 and 20")
	}
	if (before > 0 || after > 0) && p.Limit > 100 {
		return page, nil, fmt.Errorf("context search requires --limit at most 100 hits")
	}
	err := a.DB().ReadView(func(db *store.DB) error {
		var err error
		page, err = db.SearchMessagesPage(p, cursor)
		if err != nil {
			return err
		}
		page.Messages = resolveMessageSenderNamesInView(ctx, a, db, page.Messages)
		if before == 0 && after == 0 {
			return nil
		}
		contexts = make([]searchHitContext, 0, len(page.Messages))
		for _, hit := range page.Messages {
			chats, err := messageChatJIDFilter(ctx, a, hit.ChatJID)
			if err != nil {
				return err
			}
			msgs, err := db.MessageContextInChats(chats, hit, before, after)
			if err != nil {
				return err
			}
			msgs = resolveMessageSenderNamesInView(ctx, a, db, msgs)
			contexts = append(contexts, searchHitContext{ChatJID: hit.ChatJID, MsgID: hit.MsgID, Messages: msgs})
		}
		return nil
	})
	return page, contexts, err
}
func localMessageIdentifier(db *store.DB, input string, chats bool) (string, error) {
	if input == "" {
		return "", nil
	}
	jid, err := wa.ParseUserOrJID(input)
	if err == nil {
		return jid.String(), nil
	}
	// Numeric/JID-looking inputs keep their established parser error.
	explicit := false
	for _, r := range input {
		if r == '@' {
			explicit = true
			break
		}
	}
	numeric := true
	for _, r := range input {
		if (r < '0' || r > '9') && r != '+' && r != ' ' && r != '-' && r != '(' && r != ')' {
			numeric = false
			break
		}
	}
	if explicit || numeric {
		return "", err
	}
	return db.ResolveMessageName(input, chats)
}

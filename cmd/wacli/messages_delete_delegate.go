package main

// Delete-for-everyone of one of your own stored messages, delegated to a
// running `sync --follow` process while it holds the store lock. The calling
// command binds the exact stored target from a read-only view before anything
// is sent; the follow process checks that same target again when the request
// leaves the send queue, and makes one native call. Delete-for-me is not
// delegated.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/lock"
	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow/types"
)

const deleteKind = "delete"

// chatAliasLookup returns the locally stored phone-number/LID counterpart of
// a user chat, or the empty JID when none is stored.
type chatAliasLookup func(context.Context, types.JID) (types.JID, error)

type exactMessageGetter interface {
	GetMessage(chatJID, msgID string) (store.Message, error)
}

type deleteStore interface {
	exactMessageGetter
	MarkMessageRevoked(chatJID, msgID string) error
}

// tryDelegateDelete hands delete-for-everyone to a same-store follow process
// only when the store lock is held. Without a reachable delegate it returns
// the lock error unchanged; it never falls back to another connection.
func tryDelegateDelete(ctx context.Context, flags *rootFlags, lockErr error, chat, id string, postSendWait time.Duration) (sendDelegateResponse, bool, error) {
	if !lock.IsLocked(lockErr) {
		return sendDelegateResponse{}, false, lockErr
	}
	req, err := bindDelegatedDelete(ctx, flags, chat, id)
	if err != nil {
		return sendDelegateResponse{}, true, err
	}
	req.PostSendWaitMS = durationMillis(postSendWait)
	resp, err := delegateSend(ctx, flags, req)
	if err != nil {
		if errors.Is(err, errSendDelegateUnavailable) {
			return sendDelegateResponse{}, false, lockErr
		}
		return sendDelegateResponse{}, true, err
	}
	// Once the request was sent, anything but a confirmed result for the
	// bound target leaves the outcome unknown.
	if !resp.Sent || resp.ID == "" || resp.Target != req.ID || resp.To != req.TargetChat {
		return sendDelegateResponse{}, true, deleteOutcomeUnknown(errors.New("unexpected reply from the running sync process"))
	}
	return resp, true, nil
}

// bindDelegatedDelete resolves the chat and the exact stored message from a
// read-only view of the store, so the queued request names one stored row and
// a display name or mapping that changes meanwhile cannot retarget it. Alias
// lookups here are best effort; the follow process repeats them strictly.
func bindDelegatedDelete(ctx context.Context, flags *rootFlags, chat, id string) (sendDelegateRequest, error) {
	if err := validateExactMessageID(id); err != nil {
		return sendDelegateRequest{}, err
	}
	storeDir, err := resolveStoreDir(flags)
	if err != nil {
		return sendDelegateRequest{}, err
	}
	view, err := app.New(app.Options{StoreDir: storeDir, Version: effectiveVersion(), ReadOnly: true})
	if err != nil {
		return sendDelegateRequest{}, fmt.Errorf("read local store: %w; message %s was not deleted", err, id)
	}
	defer closeApp(view, nil)

	resolved, err := localMessageIdentifier(view.DB(), strings.TrimSpace(chat), true)
	if err != nil {
		return sendDelegateRequest{}, fmt.Errorf("%w; message %s was not deleted", err, id)
	}
	requested, err := wa.ParseUserOrJID(resolved)
	if err != nil {
		return sendDelegateRequest{}, fmt.Errorf("%w; message %s was not deleted", err, id)
	}
	requested = canonicalMessageFilterJID(requested)
	msg, err := findExactStoredMessage(ctx, view.DB(), readOnlyChatAlias(view), requested, id)
	if err != nil {
		return sendDelegateRequest{}, err
	}
	if err := validateMessageCanRevoke(msg); err != nil {
		return sendDelegateRequest{}, fmt.Errorf("%w; it was not deleted", err)
	}
	return sendDelegateRequest{Kind: deleteKind, To: requested.String(), TargetChat: msg.ChatJID, ID: msg.MsgID}, nil
}

// readOnlyChatAlias reads the session's stored mapping without opening a
// client. A missed alias can only make the delete refuse, here or when the
// follow process finds a row the binding did not.
func readOnlyChatAlias(view *app.App) chatAliasLookup {
	resolver, err := view.ReadOnlyResolver()
	if err != nil {
		return func(context.Context, types.JID) (types.JID, error) { return types.EmptyJID, nil }
	}
	return func(ctx context.Context, jid types.JID) (types.JID, error) {
		alias := jid
		switch jid.Server {
		case types.DefaultUserServer:
			alias = resolver.ResolvePNToLID(ctx, jid)
		case types.HiddenUserServer:
			alias = resolver.ResolveLIDToPN(ctx, jid)
		}
		if alias.ToNonAD() == jid.ToNonAD() {
			return types.EmptyJID, nil
		}
		return alias.ToNonAD(), nil
	}
}

// findExactStoredMessage returns the only stored row with exactly this ID in
// the chat or its stored phone-number/LID counterpart. No row, a failed
// lookup, or rows under more than one chat key refuse, even when the rows
// agree.
func findExactStoredMessage(ctx context.Context, db exactMessageGetter, alias chatAliasLookup, chat types.JID, id string) (store.Message, error) {
	keys := []types.JID{canonicalMessageFilterJID(chat)}
	if chat.Server == types.DefaultUserServer || chat.Server == types.HiddenUserServer {
		mapped, err := alias(ctx, chat)
		if err != nil {
			return store.Message{}, fmt.Errorf("look up the stored phone-number/LID mapping for %s: %w; message %s was not deleted", chat, err, id)
		}
		keys = append(keys, canonicalMessageFilterJID(mapped))
	}
	var found []store.Message
	for _, key := range jidStrings(keys) {
		msg, err := db.GetMessage(key, id)
		if isNoRows(err) {
			continue
		}
		if err != nil {
			return store.Message{}, fmt.Errorf("look up message %s in %s: %w; it was not deleted", id, key, err)
		}
		found = append(found, msg)
	}
	switch len(found) {
	case 0:
		return store.Message{}, fmt.Errorf("message %s is not stored for %s; it was not deleted", id, chat)
	case 1:
		return found[0], nil
	default:
		return store.Message{}, fmt.Errorf("message %s is stored under more than one identity of %s; it was not deleted", id, chat)
	}
}

func validateExactMessageID(id string) error {
	if id == "" || id != strings.TrimSpace(id) {
		return fmt.Errorf("delete requires the exact message ID without surrounding spaces; nothing was deleted")
	}
	return nil
}

// deleteOutcomeUnknown reports a failure after the delete may have reached
// WhatsApp. It never claims that nothing was deleted.
func deleteOutcomeUnknown(err error) error {
	return fmt.Errorf("delete outcome unknown: the message may already be deleted for everyone, so check the chat before retrying: %w", err)
}

func writeDelegatedDeleteOutput(flags *rootFlags, resp sendDelegateResponse) error {
	if resp.StoreWarning != "" {
		fmt.Fprintf(os.Stderr, "warning: WhatsApp accepted the delete of message %s (id %s) but the local store update failed: %s; do not retry the delete\n", resp.Target, resp.ID, resp.StoreWarning)
	}
	if flags.asJSON {
		body := map[string]any{"revoked": true, "to": resp.To, "id": resp.ID, "target": resp.Target}
		if resp.StoreWarning != "" {
			body["store_warning"] = resp.StoreWarning
		}
		return out.WriteJSON(os.Stdout, body)
	}
	fmt.Fprintf(os.Stdout, "Deleted message %s in %s (id %s)\n", resp.Target, resp.To, resp.ID)
	return nil
}

// delegatedDelete is what the follow process uses to delete its own message.
type delegatedDelete struct {
	db        deleteStore
	storeDir  string
	connected func() bool
	alias     chatAliasLookup
	revoke    func(context.Context, types.JID, types.MessageID) (types.MessageID, error)
}

type localChatAliasClient interface {
	LocalChatAlias(context.Context, types.JID) (types.JID, error)
}

func delegatedDeleteFor(a *app.App) delegatedDelete {
	return delegatedDelete{
		db:       a.DB(),
		storeDir: a.StoreDir(),
		connected: func() bool {
			client := a.WA()
			return client != nil && client.IsConnected()
		},
		alias: func(ctx context.Context, jid types.JID) (types.JID, error) {
			client, ok := a.WA().(localChatAliasClient)
			if !ok {
				return types.EmptyJID, errors.New("local LID mapping is not available")
			}
			return client.LocalChatAlias(ctx, jid)
		},
		revoke: func(ctx context.Context, chat types.JID, id types.MessageID) (types.MessageID, error) {
			return a.WA().RevokeMessage(ctx, chat, id)
		},
	}
}

func executeDelegatedDelete(ctx context.Context, d delegatedDelete, req sendDelegateRequest) (sendDelegateResponse, error) {
	msg, chatJID, err := d.revalidate(ctx, req)
	if err != nil {
		return sendDelegateResponse{}, err
	}
	if ctx.Err() != nil {
		return sendDelegateResponse{}, fmt.Errorf("request deadline passed before dispatch; message %s was not deleted", msg.MsgID)
	}
	if !d.connected() {
		return sendDelegateResponse{}, fmt.Errorf("WhatsApp is not connected; message %s was not deleted", msg.MsgID)
	}
	if err := warnRapidSendIfNeeded(d.storeDir, time.Now().UTC(), os.Stderr); err != nil {
		return sendDelegateResponse{}, fmt.Errorf("%w; message %s was not deleted", err, msg.MsgID)
	}
	// One native call, with no reconnect or retry: after a timeout or a lost
	// answer WhatsApp may already have applied the delete. The call runs on
	// this goroutine, so the send queue stays held until it has returned,
	// even when that is after its deadline.
	attemptCtx, cancel := context.WithTimeout(ctx, sendAttemptTimeout)
	sentID, err := d.revoke(attemptCtx, chatJID, types.MessageID(msg.MsgID))
	cancel()
	if err != nil {
		return sendDelegateResponse{}, deleteOutcomeUnknown(err)
	}
	resp := sendDelegateResponse{OK: true, Sent: true, To: msg.ChatJID, ID: string(sentID), Target: msg.MsgID}
	if err := d.db.MarkMessageRevoked(msg.ChatJID, msg.MsgID); err != nil {
		resp.StoreWarning = fmt.Sprintf("store deleted message state: %v", err)
	}
	waitForPostSendRetryReceipts(ctx, millisDuration(req.PostSendWaitMS, 0))
	return resp, nil
}

// revalidate checks, without resolving names, that the target the calling
// command bound is still the only stored row with that ID for the requested
// chat and its stored phone-number/LID mapping, under the same chat key, and
// still an undeleted message sent by this account.
func (d delegatedDelete) revalidate(ctx context.Context, req sendDelegateRequest) (store.Message, types.JID, error) {
	if err := validateExactMessageID(req.ID); err != nil {
		return store.Message{}, types.JID{}, err
	}
	requested, err := parseBoundChatJID(req.To)
	if err != nil {
		return store.Message{}, types.JID{}, fmt.Errorf("delegated delete needs the resolved chat JID: %w; message %s was not deleted", err, req.ID)
	}
	msg, err := findExactStoredMessage(ctx, d.db, d.alias, requested, req.ID)
	if err != nil {
		return store.Message{}, types.JID{}, err
	}
	if msg.ChatJID != req.TargetChat {
		return store.Message{}, types.JID{}, fmt.Errorf("message %s is stored under %s, not the bound chat %q; it was not deleted", req.ID, msg.ChatJID, req.TargetChat)
	}
	if err := validateMessageCanRevoke(msg); err != nil {
		return store.Message{}, types.JID{}, fmt.Errorf("%w; it was not deleted", err)
	}
	chatJID, err := wa.ParseUserOrJID(msg.ChatJID)
	if err != nil {
		return store.Message{}, types.JID{}, fmt.Errorf("stored chat JID is invalid: %w; message %s was not deleted", err, req.ID)
	}
	return msg, chatJID, nil
}

// parseBoundChatJID accepts only the canonical chat JID the calling command
// resolved, never a phone number or name to resolve again.
func parseBoundChatJID(s string) (types.JID, error) {
	jid, err := types.ParseJID(s)
	if err != nil {
		return types.JID{}, err
	}
	if jid.User == "" || jid.String() != s || canonicalMessageFilterJID(jid) != jid {
		return types.JID{}, fmt.Errorf("%q is not an exact chat JID", s)
	}
	return jid, nil
}

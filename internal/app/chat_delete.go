package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/types"
)

// Codes a refused chat delete answers with. Nothing was sent to WhatsApp.
const (
	ChatDeleteNotFound      = "not_found"
	ChatDeleteNewerMessages = "newer_messages"
	ChatDeleteNotOneToOne   = "not_one_to_one"
)

// ChatDeleteRefusal is a chat delete refused before anything reached WhatsApp.
// Code is one of the ChatDelete* codes, for callers that branch on it.
type ChatDeleteRefusal struct {
	Code   string
	Reason string
}

func (r *ChatDeleteRefusal) Error() string { return r.Code + ": " + r.Reason }

// RefusalCode lets a caller tell a refusal from a failure without this type.
func (r *ChatDeleteRefusal) RefusalCode() string { return r.Code }

// ChatDeleted is the message range a chat delete sent to WhatsApp.
type ChatDeleted struct {
	Chat types.JID
	// LastMessageID is the stored message the delete named as its last; empty
	// when the store holds none at or before the requested time.
	LastMessageID string
	// Through is the range's end: that message's time, else the requested time.
	// WhatsApp keeps the messages after it.
	Through time.Time
}

// DeleteChat deletes a one-to-one chat for this account through the given
// time. It names the newest stored message at or before that time as the last
// message, so anything after it stays, and refuses when the store holds a
// message later than the time: the caller has not seen it. The local store
// keeps the chat, as it does when the chat is deleted on the phone.
func (a *App) DeleteChat(ctx context.Context, jid types.JID, through time.Time) (ChatDeleted, error) {
	if jid.Server != types.DefaultUserServer && jid.Server != types.HiddenUserServer {
		return ChatDeleted{}, &ChatDeleteRefusal{Code: ChatDeleteNotOneToOne, Reason: fmt.Sprintf("%s is not a one-to-one chat; only those can be deleted", jid)}
	}
	if through.IsZero() {
		return ChatDeleted{}, fmt.Errorf("a chat delete needs the time it reaches through")
	}
	jid = jid.ToNonAD()

	release, err := a.beginChatStateWrite(ctx, appstate.WAPatchRegularHigh)
	if err != nil {
		return ChatDeleted{}, err
	}
	defer release()
	// Read the store after the pre-write sync, as close to the send as it gets.
	r, err := a.db.ChatDeleteRange(a.chatDeleteJIDs(ctx, jid), through)
	if err != nil {
		return ChatDeleted{}, fmt.Errorf("read chat %s for delete: %w", jid, err)
	}
	if !r.Known {
		return ChatDeleted{}, &ChatDeleteRefusal{Code: ChatDeleteNotFound, Reason: fmt.Sprintf("no chat %s is stored", jid)}
	}
	if r.Newer > 0 {
		return ChatDeleted{}, &ChatDeleteRefusal{Code: ChatDeleteNewerMessages, Reason: fmt.Sprintf(
			"%d stored message(s) in %s are later than %s, the newest at %s; nothing was deleted",
			r.Newer, jid, through.UTC().Format(time.RFC3339), r.NewestAt.Format(time.RFC3339))}
	}
	deleted := ChatDeleted{Chat: jid, Through: through.UTC().Truncate(time.Second)}
	var lastKey *waCommon.MessageKey
	if r.Last != nil {
		deleted.LastMessageID = r.Last.MsgID
		deleted.Through = r.Last.Timestamp
		lastKey = messageKeyFromStore(*r.Last)
	}

	pending, err := a.beginLocalAppStateWrite(appstate.WAPatchRegularHigh)
	if err != nil {
		return ChatDeleted{}, err
	}
	postSendEvents, err := a.wa.DeleteChat(ctx, jid, deleted.Through, lastKey, func() { pending.reserve(a) })
	if err != nil {
		return ChatDeleted{}, errors.Join(err, a.failLocalAppStateWrite(ctx, &pending, postSendEvents))
	}
	if !pending.reserved {
		return ChatDeleted{}, fmt.Errorf("WhatsApp app state send completed without an apply boundary")
	}
	if err := a.completeLocalAppStateWrite(ctx, &pending, postSendEvents, func() error { return nil }); err != nil {
		return ChatDeleted{}, err
	}
	return deleted, nil
}

// chatDeleteJIDs is every JID the store may hold the chat under: the one asked
// for and its phone-number and LID forms.
func (a *App) chatDeleteJIDs(ctx context.Context, jid types.JID) []string {
	pn := a.canonicalStoreJID(ctx, jid)
	lid := a.wa.ResolvePNToLID(ctx, pn)
	var jids []string
	seen := map[string]bool{}
	for _, j := range []types.JID{jid, pn, lid} {
		if j.IsEmpty() || (j.Server != types.DefaultUserServer && j.Server != types.HiddenUserServer) {
			continue
		}
		s := j.ToNonAD().String()
		if !seen[s] {
			seen[s] = true
			jids = append(jids, s)
		}
	}
	return jids
}

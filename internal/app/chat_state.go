package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func (a *App) ArchiveChat(ctx context.Context, jid types.JID, archive bool) error {
	chatJID := canonicalJIDString(a.canonicalStoreJID(ctx, jid))
	lastTS, lastKey := a.latestMessageRange(chatJID)
	if err := a.wa.ArchiveChat(ctx, jid, archive, lastTS, lastKey); err != nil {
		return err
	}
	return a.db.SetChatArchived(chatJID, archive)
}

func (a *App) PinChat(ctx context.Context, jid types.JID, pin bool) error {
	chatJID := canonicalJIDString(a.canonicalStoreJID(ctx, jid))
	if err := a.wa.PinChat(ctx, jid, pin); err != nil {
		return err
	}
	return a.db.SetChatPinned(chatJID, pin)
}

func (a *App) MuteChat(ctx context.Context, jid types.JID, mute bool, duration time.Duration) error {
	chatJID := canonicalJIDString(a.canonicalStoreJID(ctx, jid))
	if err := a.wa.MuteChat(ctx, jid, mute, duration); err != nil {
		return err
	}
	return a.db.SetChatMutedUntil(chatJID, mutedUntilUnix(mute, duration, nowUTC()))
}

func (a *App) MarkChatRead(ctx context.Context, jid types.JID, read bool) error {
	chatJID := canonicalJIDString(a.canonicalStoreJID(ctx, jid))
	lastTS, lastKey := a.latestMessageRange(chatJID)
	if err := a.wa.MarkChatAsRead(ctx, jid, read, lastTS, lastKey); err != nil {
		return err
	}
	return a.db.SetChatUnread(chatJID, !read)
}

func (a *App) latestMessageRange(chatJID string) (time.Time, *waCommon.MessageKey) {
	info, err := a.db.GetLatestMessageInfo(chatJID)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			a.emitWarning(
				"chat_state_latest_message_failed",
				fmt.Sprintf("warning: failed to load latest message for chat state patch: %v", err),
				map[string]any{"chat_jid": chatJID, "error": err.Error()},
			)
		}
		return time.Time{}, nil
	}
	return info.Timestamp, messageKeyFromStore(info)
}

func messageKeyFromStore(info store.MessageInfo) *waCommon.MessageKey {
	if strings.TrimSpace(info.ChatJID) == "" || strings.TrimSpace(info.MsgID) == "" {
		return nil
	}
	key := &waCommon.MessageKey{
		RemoteJID: proto.String(info.ChatJID),
		FromMe:    proto.Bool(info.FromMe),
		ID:        proto.String(info.MsgID),
	}
	if sender := strings.TrimSpace(info.SenderJID); sender != "" && sender != info.ChatJID {
		key.Participant = proto.String(sender)
	}
	return key
}

func (a *App) handleChatStateEvent(ctx context.Context, evt interface{}) {
	switch v := evt.(type) {
	case *events.Archive:
		if v == nil || v.JID.IsEmpty() || v.Action == nil {
			return
		}
		chat := a.canonicalStoreJID(ctx, v.JID)
		if err := a.db.SetChatArchived(canonicalJIDString(chat), v.Action.GetArchived()); err != nil {
			a.emitChatStateWarning("archive", v.JID, err)
		}
	case *events.Pin:
		if v == nil || v.JID.IsEmpty() || v.Action == nil {
			return
		}
		chat := a.canonicalStoreJID(ctx, v.JID)
		if err := a.db.SetChatPinned(canonicalJIDString(chat), v.Action.GetPinned()); err != nil {
			a.emitChatStateWarning("pin", v.JID, err)
		}
	case *events.Mute:
		if v == nil || v.JID.IsEmpty() || v.Action == nil {
			return
		}
		chat := a.canonicalStoreJID(ctx, v.JID)
		if err := a.db.SetChatMutedUntil(canonicalJIDString(chat), mutedUntilFromAction(v.Action)); err != nil {
			a.emitChatStateWarning("mute", v.JID, err)
		}
	case *events.MarkChatAsRead:
		if v == nil || v.JID.IsEmpty() || v.Action == nil {
			return
		}
		chat := a.canonicalStoreJID(ctx, v.JID)
		if err := a.db.SetChatUnread(canonicalJIDString(chat), !v.Action.GetRead()); err != nil {
			a.emitChatStateWarning("mark_read", v.JID, err)
		}
	}
}

func mutedUntilFromAction(action *waSyncAction.MuteAction) int64 {
	if action == nil || !action.GetMuted() {
		return 0
	}
	ms := action.GetMuteEndTimestamp()
	if ms < 0 {
		return -1
	}
	if ms > 0 {
		return time.UnixMilli(ms).Unix()
	}
	return -1
}

func mutedUntilUnix(mute bool, duration time.Duration, base time.Time) int64 {
	if !mute {
		return 0
	}
	if duration <= 0 {
		return -1
	}
	return base.Add(duration).Unix()
}

func (a *App) emitChatStateWarning(kind string, jid types.JID, err error) {
	a.emitWarning(
		"chat_state_store_failed",
		fmt.Sprintf("warning: failed to store %s chat state for %s: %v", kind, jid, err),
		map[string]any{"kind": kind, "jid": jid.String(), "error": err.Error()},
	)
}

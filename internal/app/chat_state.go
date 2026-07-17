package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

const (
	appStateRetryInitialDelay = 250 * time.Millisecond
	appStateRetryMaxDelay     = 5 * time.Second
)

func (a *App) ArchiveChat(ctx context.Context, jid types.JID, archive bool) error {
	if err := a.syncChatStateBeforeWrite(ctx, appstate.WAPatchRegularLow); err != nil {
		return err
	}
	chatJID := canonicalJIDString(a.canonicalStoreJID(ctx, jid))
	lastTS, lastKey := a.latestMessageRange(chatJID)
	if err := a.wa.ArchiveChat(ctx, jid, archive, lastTS, lastKey); err != nil {
		return err
	}
	return a.db.SetChatArchived(chatJID, archive)
}

func (a *App) PinChat(ctx context.Context, jid types.JID, pin bool) error {
	if err := a.syncChatStateBeforeWrite(ctx, appstate.WAPatchRegularLow); err != nil {
		return err
	}
	chatJID := canonicalJIDString(a.canonicalStoreJID(ctx, jid))
	if err := a.wa.PinChat(ctx, jid, pin); err != nil {
		return err
	}
	return a.db.SetChatPinned(chatJID, pin)
}

func (a *App) MuteChat(ctx context.Context, jid types.JID, mute bool, duration time.Duration) error {
	if err := a.syncChatStateBeforeWrite(ctx, appstate.WAPatchRegularHigh); err != nil {
		return err
	}
	chatJID := canonicalJIDString(a.canonicalStoreJID(ctx, jid))
	if err := a.wa.MuteChat(ctx, jid, mute, duration); err != nil {
		return err
	}
	return a.db.SetChatMutedUntil(chatJID, mutedUntilUnix(mute, duration, nowUTC()))
}

func (a *App) MarkChatRead(ctx context.Context, jid types.JID, read bool) error {
	if err := a.syncChatStateBeforeWrite(ctx, appstate.WAPatchRegularLow); err != nil {
		return err
	}
	chatJID := canonicalJIDString(a.canonicalStoreJID(ctx, jid))
	lastTS, lastKey := a.latestMessageRange(chatJID)
	if err := a.wa.MarkChatAsRead(ctx, jid, read, lastTS, lastKey); err != nil {
		return err
	}
	return a.db.SetChatUnread(chatJID, !read)
}

func (a *App) syncChatStateBeforeWrite(ctx context.Context, collection appstate.WAPatchName) error {
	select {
	case <-ctx.Done():
		return fmt.Errorf("wait for chat state synchronization: %w", ctx.Err())
	case <-a.chatStateSync:
	}
	defer func() { a.chatStateSync <- struct{}{} }()

	tracker := &appStatePersistenceTracker{}
	handlerID := a.wa.AddEventHandler(func(evt interface{}) {
		a.handleAppStatePersistenceEvent(ctx, evt, tracker)
	})
	defer a.wa.RemoveEventHandler(handlerID)
	recoveryRequired, err := a.db.AppStateRecoveryRequired(string(collection))
	if err != nil {
		return fmt.Errorf("check WhatsApp app state recovery for %s: %w", collection, err)
	}
	if recoveryRequired {
		return a.replayRequiredAppState(ctx, collection, tracker)
	}

	retryDelay := appStateRetryInitialDelay
	for {
		err := a.wa.FetchAppState(ctx, string(collection), false, false)
		if err == nil {
			return nil
		}

		waitErr := "wait for missing WhatsApp app state key"
		if errors.Is(err, appstate.ErrMismatchingLTHash) {
			if err := a.db.MarkAppStateRecoveryRequired(string(collection)); err != nil {
				return fmt.Errorf("mark WhatsApp app state recovery for %s: %w", collection, err)
			}
			return a.replayRequiredAppState(ctx, collection, tracker)
		} else if !errors.Is(err, appstate.ErrKeyNotFound) {
			return fmt.Errorf("sync WhatsApp chat state before update: %w", err)
		}

		// Missing keys and recovery snapshots arrive asynchronously from the
		// primary device, so keep the connection alive and retry the state fetch.
		timer := time.NewTimer(retryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("%s: %w", waitErr, ctx.Err())
		case <-timer.C:
		}
		if retryDelay < appStateRetryMaxDelay {
			retryDelay *= 2
			if retryDelay > appStateRetryMaxDelay {
				retryDelay = appStateRetryMaxDelay
			}
		}
	}
}

func (a *App) replayRequiredAppState(ctx context.Context, collection appstate.WAPatchName, tracker *appStatePersistenceTracker) error {
	retryDelay := appStateRetryInitialDelay
	for {
		tracker.begin()
		fetchErr := a.wa.FetchAppState(ctx, string(collection), true, false)
		persistenceErr := tracker.end()
		if persistenceErr != nil {
			return fmt.Errorf("persist replayed app state %s: %w", collection, persistenceErr)
		}
		if fetchErr == nil {
			if err := a.db.ClearAppStateRecoveryRequired(string(collection)); err != nil {
				return fmt.Errorf("clear WhatsApp app state recovery for %s: %w", collection, err)
			}
			return nil
		}
		if !errors.Is(fetchErr, appstate.ErrKeyNotFound) {
			return fmt.Errorf("replay WhatsApp app state recovery for %s: %w", collection, fetchErr)
		}

		timer := time.NewTimer(retryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("wait for missing WhatsApp app state key during %s recovery: %w", collection, ctx.Err())
		case <-timer.C:
		}
		if retryDelay < appStateRetryMaxDelay {
			retryDelay *= 2
			if retryDelay > appStateRetryMaxDelay {
				retryDelay = appStateRetryMaxDelay
			}
		}
	}
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

func (a *App) handleChatStateEvent(ctx context.Context, evt interface{}) error {
	switch v := evt.(type) {
	case *events.Archive:
		if v == nil || v.JID.IsEmpty() || v.Action == nil {
			return nil
		}
		chat := a.canonicalStoreJID(ctx, v.JID)
		if err := a.db.SetChatArchived(canonicalJIDString(chat), v.Action.GetArchived()); err != nil {
			a.emitChatStateWarning("archive", v.JID, err)
			return err
		}
	case *events.Pin:
		if v == nil || v.JID.IsEmpty() || v.Action == nil {
			return nil
		}
		chat := a.canonicalStoreJID(ctx, v.JID)
		if err := a.db.SetChatPinned(canonicalJIDString(chat), v.Action.GetPinned()); err != nil {
			a.emitChatStateWarning("pin", v.JID, err)
			return err
		}
	case *events.Mute:
		if v == nil || v.JID.IsEmpty() || v.Action == nil {
			return nil
		}
		chat := a.canonicalStoreJID(ctx, v.JID)
		if err := a.db.SetChatMutedUntil(canonicalJIDString(chat), mutedUntilFromAction(v.Action)); err != nil {
			a.emitChatStateWarning("mute", v.JID, err)
			return err
		}
	case *events.MarkChatAsRead:
		if v == nil || v.JID.IsEmpty() || v.Action == nil {
			return nil
		}
		chat := a.canonicalStoreJID(ctx, v.JID)
		if err := a.db.SetChatUnread(canonicalJIDString(chat), !v.Action.GetRead()); err != nil {
			a.emitChatStateWarning("mark_read", v.JID, err)
			return err
		}
	}
	return nil
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

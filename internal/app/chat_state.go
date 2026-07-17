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
	release, err := a.beginChatStateWrite(ctx, appstate.WAPatchRegularLow)
	if err != nil {
		return err
	}
	defer release()
	chatJID := canonicalJIDString(a.canonicalStoreJID(ctx, jid))
	lastTS, lastKey := a.latestMessageRange(chatJID)
	pending, err := a.beginLocalAppStateWrite(appstate.WAPatchRegularLow)
	if err != nil {
		return err
	}
	postSendEvents, err := a.wa.ArchiveChat(ctx, jid, archive, lastTS, lastKey)
	if err != nil {
		a.abandonLocalAppStateWrite(pending)
		return err
	}
	return a.completeLocalAppStateWrite(ctx, pending, postSendEvents, func() error {
		return a.db.SetChatArchived(chatJID, archive)
	})
}

func (a *App) PinChat(ctx context.Context, jid types.JID, pin bool) error {
	release, err := a.beginChatStateWrite(ctx, appstate.WAPatchRegularLow)
	if err != nil {
		return err
	}
	defer release()
	chatJID := canonicalJIDString(a.canonicalStoreJID(ctx, jid))
	pending, err := a.beginLocalAppStateWrite(appstate.WAPatchRegularLow)
	if err != nil {
		return err
	}
	postSendEvents, err := a.wa.PinChat(ctx, jid, pin)
	if err != nil {
		a.abandonLocalAppStateWrite(pending)
		return err
	}
	return a.completeLocalAppStateWrite(ctx, pending, postSendEvents, func() error {
		return a.db.SetChatPinned(chatJID, pin)
	})
}

func (a *App) MuteChat(ctx context.Context, jid types.JID, mute bool, duration time.Duration) error {
	release, err := a.beginChatStateWrite(ctx, appstate.WAPatchRegularHigh)
	if err != nil {
		return err
	}
	defer release()
	chatJID := canonicalJIDString(a.canonicalStoreJID(ctx, jid))
	mutedUntil := mutedUntilUnix(mute, duration, nowUTC())
	pending, err := a.beginLocalAppStateWrite(appstate.WAPatchRegularHigh)
	if err != nil {
		return err
	}
	postSendEvents, err := a.wa.MuteChat(ctx, jid, mute, duration)
	if err != nil {
		a.abandonLocalAppStateWrite(pending)
		return err
	}
	return a.completeLocalAppStateWrite(ctx, pending, postSendEvents, func() error {
		return a.db.SetChatMutedUntil(chatJID, mutedUntil)
	})
}

func (a *App) MarkChatRead(ctx context.Context, jid types.JID, read bool) error {
	release, err := a.beginChatStateWrite(ctx, appstate.WAPatchRegularLow)
	if err != nil {
		return err
	}
	defer release()
	chatJID := canonicalJIDString(a.canonicalStoreJID(ctx, jid))
	lastTS, lastKey := a.latestMessageRange(chatJID)
	pending, err := a.beginLocalAppStateWrite(appstate.WAPatchRegularLow)
	if err != nil {
		return err
	}
	postSendEvents, err := a.wa.MarkChatAsRead(ctx, jid, read, lastTS, lastKey)
	if err != nil {
		a.abandonLocalAppStateWrite(pending)
		return err
	}
	return a.completeLocalAppStateWrite(ctx, pending, postSendEvents, func() error {
		return a.db.SetChatUnread(chatJID, !read)
	})
}

func (a *App) beginChatStateWrite(ctx context.Context, collection appstate.WAPatchName) (func(), error) {
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("wait for chat state synchronization: %w", ctx.Err())
	case <-a.chatStateSync:
	}
	release := func() { a.chatStateSync <- struct{}{} }
	if err := a.syncChatStateBeforeWrite(ctx, collection); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

func (a *App) syncChatStateBeforeWrite(ctx context.Context, collection appstate.WAPatchName) error {
	markerGeneration, recoveryRequired, err := a.db.BeginAppStateRecovery(string(collection))
	if err != nil {
		return fmt.Errorf("begin WhatsApp app state recovery for %s: %w", collection, err)
	}
	tracker := &appStatePersistenceTracker{}
	if recoveryRequired {
		return a.replayRequiredAppState(ctx, collection, markerGeneration, tracker)
	}

	for {
		err, persistenceErr := a.fetchAndPersistAppState(ctx, collection, false, tracker)
		if persistenceErr != nil {
			return fmt.Errorf("persist fetched app state %s: %w", collection, persistenceErr)
		}
		if err == nil {
			return a.clearCompletedAppStateRecovery(collection, markerGeneration)
		}

		if errors.Is(err, appstate.ErrMismatchingLTHash) {
			return a.replayRequiredAppState(ctx, collection, markerGeneration, tracker)
		} else if errors.Is(err, appstate.ErrKeyNotFound) {
			return a.replayRequiredAppState(ctx, collection, markerGeneration, tracker)
		} else {
			return fmt.Errorf("sync WhatsApp chat state before update: %w", err)
		}
	}
}

func (a *App) replayRequiredAppState(ctx context.Context, collection appstate.WAPatchName, markerGeneration int64, tracker *appStatePersistenceTracker) error {
	retryDelay := appStateRetryInitialDelay
	for {
		fetchErr, persistenceErr := a.fetchAndPersistAppState(ctx, collection, true, tracker)
		if persistenceErr != nil {
			return fmt.Errorf("persist replayed app state %s: %w", collection, persistenceErr)
		}
		if fetchErr == nil {
			return a.clearCompletedAppStateRecovery(collection, markerGeneration)
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

func (a *App) fetchAndPersistAppState(ctx context.Context, collection appstate.WAPatchName, fullSync bool, tracker *appStatePersistenceTracker) (fetchErr, persistenceErr error) {
	ticket := a.appStatePersist.reserve()
	eventsToPersist, fetchErr := a.wa.FetchAppStateEvents(ctx, string(collection), fullSync, false)
	result := make(chan error, 1)
	frontier := a.appStatePersist.complete(ticket, func() {
		result <- a.persistFetchedAppStateEvents(ctx, eventsToPersist, tracker)
	})
	if waitErr := a.appStatePersist.waitThrough(ctx, frontier); waitErr != nil {
		return fetchErr, waitErr
	}
	return fetchErr, <-result
}

func (a *App) clearCompletedAppStateRecovery(collection appstate.WAPatchName, markerGeneration int64) error {
	cleared, err := a.db.ClearAppStateRecoveryGeneration(string(collection), markerGeneration)
	if err != nil {
		return fmt.Errorf("clear WhatsApp app state recovery for %s: %w", collection, err)
	}
	if !cleared {
		required, checkErr := a.db.AppStateRecoveryRequired(string(collection))
		if checkErr != nil {
			return fmt.Errorf("check completed WhatsApp app state recovery for %s: %w", collection, checkErr)
		}
		if required {
			return fmt.Errorf("WhatsApp app state recovery changed during %s synchronization; retry the command", collection)
		}
	}
	return nil
}

type pendingLocalAppStateWrite struct {
	ticket uint64
}

func (a *App) beginLocalAppStateWrite(collection appstate.WAPatchName) (pendingLocalAppStateWrite, error) {
	pending := pendingLocalAppStateWrite{
		ticket: a.appStatePersist.reserve(),
	}
	err := a.db.MarkAppStateRecoveryRequired(string(collection))
	if err != nil {
		a.appStatePersist.completeOne(pending.ticket, func() {})
		return pendingLocalAppStateWrite{}, fmt.Errorf("mark local WhatsApp app state recovery for %s: %w", collection, err)
	}
	return pending, nil
}

func (a *App) abandonLocalAppStateWrite(pending pendingLocalAppStateWrite) {
	// A send error is ambiguous: whatsmeow may already have advanced its app
	// state cursor. Leave the durable intent for the next full replay.
	a.appStatePersist.completeOne(pending.ticket, func() {})
}

func (a *App) completeLocalAppStateWrite(ctx context.Context, pending pendingLocalAppStateWrite, postSendEvents []interface{}, persist func() error) error {
	postSendTicket := a.appStatePersist.reserve()
	result := make(chan error, 1)
	persistCtx := context.WithoutCancel(ctx)
	var localErr error
	a.appStatePersist.completeOne(pending.ticket, func() {
		localErr = persist()
	})
	frontier := a.appStatePersist.complete(postSendTicket, func() {
		tracker := &appStatePersistenceTracker{}
		eventsErr := a.persistFetchedAppStateEvents(persistCtx, postSendEvents, tracker)
		persistErr := localErr
		if persistErr == nil {
			persistErr = eventsErr
		}
		result <- persistErr
	})
	if err := a.appStatePersist.waitThrough(persistCtx, frontier); err != nil {
		return err
	}
	// Another whatsmeow fetch can advance the cursor and dispatch later. Keep
	// replay debt durable until the next pre-write full replay.
	return <-result
}

func (a *App) persistFetchedAppStateEvents(ctx context.Context, eventsToPersist []interface{}, tracker *appStatePersistenceTracker) error {
	tracker.begin()
	for _, evt := range eventsToPersist {
		a.handleAppStatePersistenceEvent(ctx, evt, tracker)
	}
	return tracker.end()
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

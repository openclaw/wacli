package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/steipete/wacli/internal/store"
	"github.com/steipete/wacli/internal/wa"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types/events"
)

func newMediaEnqueuer(ctx context.Context, jobs chan<- mediaJob) func(chatJID, msgID string) {
	return func(chatJID, msgID string) {
		if strings.TrimSpace(chatJID) == "" || strings.TrimSpace(msgID) == "" {
			return
		}
		select {
		case jobs <- mediaJob{chatJID: chatJID, msgID: msgID}:
		case <-ctx.Done():
		}
	}
}

func (a *App) addSyncEventHandler(ctx context.Context, opts SyncOptions, messagesStored, lastEvent *atomic.Int64, disconnected chan<- struct{}, enqueueMedia func(string, string), limits *syncStorageLimits) uint32 {
	var panicCount atomic.Int64
	var appStateRecoveries sync.Map
	return a.wa.AddEventHandler(func(evt interface{}) {
		// Recover from panics so unexpected message structures do not crash the
		// process. Include event type, stack trace, and a running counter.
		defer func() {
			if r := recover(); r != nil {
				n := panicCount.Add(1)
				if a.eventsEnabled() {
					a.emitEvent("event_handler_panic", map[string]any{
						"total": n,
						"event": fmt.Sprintf("%T", evt),
						"panic": fmt.Sprint(r),
						"stack": string(debug.Stack()),
					})
				} else {
					fmt.Fprintf(os.Stderr, "\nevent handler panic (recovered, total=%d) event=%T: %v\n%s\n",
						n, evt, r, debug.Stack())
				}
			}
		}()
		switch v := evt.(type) {
		case *events.Message:
			lastEvent.Store(nowUTC().UnixNano())
			a.handleLiveSyncMessage(ctx, opts, v, messagesStored, enqueueMedia, limits)
		case *events.HistorySync:
			lastEvent.Store(nowUTC().UnixNano())
			a.handleHistorySync(ctx, opts, v, messagesStored, lastEvent, enqueueMedia, limits)
		case *events.Star:
			lastEvent.Store(nowUTC().UnixNano())
			a.handleStarEvent(ctx, v)
		case *events.Connected:
			a.emitOrPrint("connected", nil, "\nConnected.\n")
		case *events.Disconnected:
			a.emitOrPrint("disconnected", nil, "\nDisconnected.\n")
			select {
			case disconnected <- struct{}{}:
			default:
			}
		case *events.AppStateSyncError:
			a.handleAppStateSyncError(ctx, v, &appStateRecoveries)
		}
	})
}

func (a *App) handleStarEvent(ctx context.Context, evt *events.Star) {
	if evt == nil || evt.ChatJID.IsEmpty() || strings.TrimSpace(evt.MessageID) == "" || evt.Action == nil {
		return
	}
	senderJID := ""
	if !evt.SenderJID.IsEmpty() {
		senderJID = canonicalJIDString(a.canonicalStoreJID(ctx, evt.SenderJID))
	}
	if err := a.db.SetStarred(store.SetStarredParams{
		ChatJID:   canonicalJIDString(a.canonicalStoreJID(ctx, evt.ChatJID)),
		MsgID:     evt.MessageID,
		SenderJID: senderJID,
		FromMe:    evt.IsFromMe,
		Starred:   evt.Action.GetStarred(),
		StarredAt: evt.Timestamp,
	}); err != nil {
		a.emitWarning(
			"starred_store_failed",
			fmt.Sprintf("warning: failed to store starred state for message %s: %v", evt.MessageID, err),
			map[string]any{"message_id": evt.MessageID, "error": err.Error()},
		)
	}
}

func (a *App) handleAppStateSyncError(ctx context.Context, evt *events.AppStateSyncError, recoveries *sync.Map) {
	if evt == nil || !errors.Is(evt.Error, appstate.ErrMismatchingLTHash) {
		return
	}
	name := strings.TrimSpace(string(evt.Name))
	if name == "" {
		return
	}
	if recoveries == nil {
		recoveries = &sync.Map{}
	}
	if _, loaded := recoveries.LoadOrStore(name, struct{}{}); loaded {
		return
	}

	a.emitWarning(
		"app_state_lthash_mismatch",
		fmt.Sprintf("warning: app state %s hit an LTHash mismatch; requesting recovery snapshot", name),
		map[string]any{"name": name},
	)
	go func() {
		reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		reqID, err := a.wa.RequestAppStateRecovery(reqCtx, name)
		if err != nil {
			a.emitWarning(
				"app_state_recovery_failed",
				fmt.Sprintf("warning: app state %s recovery request failed: %v", name, err),
				map[string]any{"name": name, "error": err.Error()},
			)
			return
		}
		if a.eventsEnabled() {
			a.emitEvent("app_state_recovery_requested", map[string]any{"name": name, "id": string(reqID)})
		} else {
			fmt.Fprintf(os.Stderr, "\rRequested app state %s recovery (id %s)\n", name, reqID)
		}
	}()
}

func (a *App) handleLiveSyncMessage(ctx context.Context, opts SyncOptions, v *events.Message, messagesStored *atomic.Int64, enqueueMedia func(string, string), limits ...*syncStorageLimits) {
	if historySyncNotificationFromMessage(v) != nil {
		return
	}
	pm := wa.ParseLiveMessage(v)
	if pm.ReactionToID != "" && pm.ReactionEmoji == "" && v.Message != nil && v.Message.GetEncReactionMessage() != nil {
		a.decryptEncryptedReaction(ctx, &pm, v)
	}
	if err := a.storeParsedMessageForSync(ctx, pm, limits...); err == nil {
		a.emitSyncProgress(messagesStored.Add(1))
	}
	if opts.DownloadMedia && pm.Media != nil && pm.ID != "" {
		enqueueMedia(pm.Chat.String(), pm.ID)
	}
}

func historySyncNotificationFromMessage(v *events.Message) *waE2E.HistorySyncNotification {
	if v == nil || v.Message == nil {
		return nil
	}
	return v.Message.GetProtocolMessage().GetHistorySyncNotification()
}

func (a *App) handleHistorySync(ctx context.Context, opts SyncOptions, v *events.HistorySync, messagesStored, lastEvent *atomic.Int64, enqueueMedia func(string, string), limits ...*syncStorageLimits) {
	a.emitOrPrint("history_sync", map[string]any{"conversations": len(v.Data.Conversations)}, "\nProcessing history sync (%d conversations)...\n", len(v.Data.Conversations))
	for _, conv := range v.Data.Conversations {
		lastEvent.Store(nowUTC().UnixNano())
		chatID := strings.TrimSpace(conv.GetID())
		if chatID == "" {
			continue
		}
		for _, m := range conv.Messages {
			lastEvent.Store(nowUTC().UnixNano())
			if m.Message == nil {
				continue
			}
			pm := wa.ParseHistoryMessage(chatID, m.Message)
			if pm.ID == "" || pm.Chat.IsEmpty() {
				continue
			}
			if pm.ReactionToID != "" && pm.ReactionEmoji == "" && m.Message.GetMessage().GetEncReactionMessage() != nil {
				evt, err := a.wa.ParseWebMessage(pm.Chat, m.Message)
				if err != nil {
					a.emitWarning(
						"encrypted_reaction_parse_failed",
						fmt.Sprintf("warning: failed to parse encrypted reaction message %s: %v", pm.ID, err),
						map[string]any{"message_id": pm.ID, "error": err.Error()},
					)
				} else {
					a.decryptEncryptedReaction(ctx, &pm, evt)
				}
			}
			if err := a.storeParsedMessageForSync(ctx, pm, limits...); err == nil {
				a.emitSyncProgress(messagesStored.Add(1))
			} else if ctx.Err() != nil {
				return
			}
			if opts.DownloadMedia && pm.Media != nil && pm.ID != "" {
				enqueueMedia(pm.Chat.String(), pm.ID)
			}
		}
	}
	if !a.eventsEnabled() {
		fmt.Fprintf(os.Stderr, "\rSynced %d messages...", messagesStored.Load())
	}
}

func (a *App) emitSyncProgress(total int64) {
	if total <= 0 || total%25 != 0 {
		return
	}
	a.emitOrPrint("progress", map[string]any{"messages_synced": total}, "\rSynced %d messages...", total)
}

func (a *App) storeParsedMessageForSync(ctx context.Context, pm wa.ParsedMessage, limits ...*syncStorageLimits) error {
	if len(limits) > 0 && limits[0] != nil {
		return limits[0].StoreParsedMessage(ctx, pm)
	}
	return a.storeParsedMessage(ctx, pm)
}

func (a *App) decryptEncryptedReaction(ctx context.Context, pm *wa.ParsedMessage, msg *events.Message) {
	reaction, err := a.wa.DecryptReaction(ctx, msg)
	if err != nil {
		a.emitWarning(
			"encrypted_reaction_decrypt_failed",
			fmt.Sprintf("warning: failed to decrypt reaction message %s: %v", pm.ID, err),
			map[string]any{"message_id": pm.ID, "error": err.Error()},
		)
		return
	}
	if reaction == nil {
		return
	}
	pm.ReactionEmoji = reaction.GetText()
	if pm.ReactionToID == "" {
		if key := reaction.GetKey(); key != nil {
			pm.ReactionToID = key.GetID()
		}
	}
}

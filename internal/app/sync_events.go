package app

import (
	"context"
	"fmt"
	"os"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"time"

	"github.com/steipete/wacli/internal/wa"
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

func (a *App) addSyncEventHandler(ctx context.Context, opts SyncOptions, messagesStored, lastEvent *atomic.Int64, disconnected chan<- struct{}, enqueueMedia func(string, string)) uint32 {
	var panicCount atomic.Int64
	return a.wa.AddEventHandler(func(evt interface{}) {
		// Recover from panics so unexpected message structures do not crash the
		// process. Include event type, stack trace, and a running counter.
		defer func() {
			if r := recover(); r != nil {
				n := panicCount.Add(1)
				fmt.Fprintf(os.Stderr, "\nevent handler panic (recovered, total=%d) event=%T: %v\n%s\n",
					n, evt, r, debug.Stack())
			}
		}()
		switch v := evt.(type) {
		case *events.Message:
			lastEvent.Store(time.Now().UTC().UnixNano())
			a.handleLiveSyncMessage(ctx, opts, v, messagesStored, enqueueMedia)
		case *events.HistorySync:
			lastEvent.Store(time.Now().UTC().UnixNano())
			a.handleHistorySync(ctx, opts, v, messagesStored, lastEvent, enqueueMedia)
		case *events.Connected:
			fmt.Fprintln(os.Stderr, "\nConnected.")
		case *events.Disconnected:
			fmt.Fprintln(os.Stderr, "\nDisconnected.")
			select {
			case disconnected <- struct{}{}:
			default:
			}
		}
	})
}

func (a *App) handleLiveSyncMessage(ctx context.Context, opts SyncOptions, v *events.Message, messagesStored *atomic.Int64, enqueueMedia func(string, string)) {
	pm := wa.ParseLiveMessage(v)
	if pm.ReactionToID != "" && pm.ReactionEmoji == "" && v.Message != nil && v.Message.GetEncReactionMessage() != nil {
		if reaction, err := a.wa.DecryptReaction(ctx, v); err == nil && reaction != nil {
			pm.ReactionEmoji = reaction.GetText()
			if pm.ReactionToID == "" {
				if key := reaction.GetKey(); key != nil {
					pm.ReactionToID = key.GetID()
				}
			}
		}
	}
	if err := a.storeParsedMessage(ctx, pm); err == nil {
		messagesStored.Add(1)
	}
	if opts.DownloadMedia && pm.Media != nil && pm.ID != "" {
		enqueueMedia(pm.Chat.String(), pm.ID)
	}
	if messagesStored.Load()%25 == 0 {
		fmt.Fprintf(os.Stderr, "\rSynced %d messages...", messagesStored.Load())
	}
}

func (a *App) handleHistorySync(ctx context.Context, opts SyncOptions, v *events.HistorySync, messagesStored, lastEvent *atomic.Int64, enqueueMedia func(string, string)) {
	fmt.Fprintf(os.Stderr, "\nProcessing history sync (%d conversations)...\n", len(v.Data.Conversations))
	for _, conv := range v.Data.Conversations {
		lastEvent.Store(time.Now().UTC().UnixNano())
		chatID := strings.TrimSpace(conv.GetID())
		if chatID == "" {
			continue
		}
		for _, m := range conv.Messages {
			lastEvent.Store(time.Now().UTC().UnixNano())
			if m.Message == nil {
				continue
			}
			pm := wa.ParseHistoryMessage(chatID, m.Message)
			if pm.ID == "" || pm.Chat.IsEmpty() {
				continue
			}
			if err := a.storeParsedMessage(ctx, pm); err == nil {
				messagesStored.Add(1)
			}
			if opts.DownloadMedia && pm.Media != nil && pm.ID != "" {
				enqueueMedia(pm.Chat.String(), pm.ID)
			}
		}
	}
	fmt.Fprintf(os.Stderr, "\rSynced %d messages...", messagesStored.Load())
}

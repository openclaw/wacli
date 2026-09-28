package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// ErrPresenceQuiet is returned when a presence subscription is asked of a
// sync that never marks this device available: WhatsApp sends presence only to
// devices that are, so the subscription would never report anything.
var ErrPresenceQuiet = errors.New("this sync runs with --presence-mode quiet, and WhatsApp sends presence only to devices that show as online")

// maxWatchedPresences bounds how many subscriptions a long-running sync renews
// after each reconnect. The oldest one is dropped first.
const maxWatchedPresences = 256

// PresenceState is what WhatsApp said about a user's presence.
type PresenceState struct {
	JID    string `json:"jid"`
	Online bool   `json:"online"`
	// LastSeen is empty while the user is online, and when WhatsApp withholds
	// it (the user hides it, or this account hides its own).
	LastSeen *time.Time `json:"last_seen,omitempty"`
}

// presenceWatch remembers whose presence a long-running sync was asked to
// watch. WhatsApp forgets presence subscriptions together with the connection
// that made them, so every new connection asks again.
type presenceWatch struct {
	mu    sync.Mutex
	jids  []types.JID // oldest first
	quiet bool
}

func (w *presenceWatch) setQuiet(quiet bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.quiet = quiet
}

func (w *presenceWatch) isQuiet() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.quiet
}

func (w *presenceWatch) add(jid types.JID) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for i, known := range w.jids {
		if known == jid {
			w.jids = append(w.jids[:i], w.jids[i+1:]...)
			break
		}
	}
	w.jids = append(w.jids, jid)
	if over := len(w.jids) - maxWatchedPresences; over > 0 {
		w.jids = append([]types.JID(nil), w.jids[over:]...)
	}
}

func (w *presenceWatch) list() []types.JID {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]types.JID(nil), w.jids...)
}

// WatchPresence asks WhatsApp to send a user's presence (online, last seen) to
// this device, and remembers the user so a running sync asks again after every
// reconnect. With wait > 0 it also waits that long for WhatsApp's first answer
// and returns it; nil means none came in time, which is what happens when the
// user hides their presence from this account.
func (a *App) WatchPresence(ctx context.Context, jid types.JID, wait time.Duration) (*PresenceState, error) {
	if jid.Server != types.DefaultUserServer && jid.Server != types.HiddenUserServer {
		return nil, fmt.Errorf("presence can only be watched for a user, not %s", jid)
	}
	if a.presenceWatch.isQuiet() {
		return nil, ErrPresenceQuiet
	}
	jid = jid.ToNonAD()
	target := a.canonicalStoreJID(ctx, jid)
	// The answer can come before SubscribePresence returns, so the handler
	// goes in first. WhatsApp may address it to the user's LID.
	answers := make(chan PresenceState, 1)
	if wait > 0 {
		handlerID := a.wa.AddEventHandler(func(evt any) {
			p, ok := evt.(*events.Presence)
			if !ok || a.canonicalStoreJID(ctx, p.From) != target {
				return
			}
			select {
			case answers <- presenceStateFromEvent(target, p):
			default:
			}
		})
		defer a.wa.RemoveEventHandler(handlerID)
	}
	if err := a.wa.SubscribePresence(ctx, jid); err != nil {
		return nil, fmt.Errorf("subscribe to presence of %s: %w", jid, err)
	}
	a.presenceWatch.add(jid)
	if wait <= 0 {
		return nil, nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case state := <-answers:
		return &state, nil
	case <-timer.C:
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// rewatchPresence renews every remembered subscription on a new connection,
// once it has marked this device available.
func (a *App) rewatchPresence(ctx context.Context) {
	for _, jid := range a.presenceWatch.list() {
		if err := a.wa.SubscribePresence(ctx, jid); err != nil {
			a.emitWarning("presence_subscribe_failed",
				fmt.Sprintf("warning: could not watch the presence of %s again after reconnecting: %v", jid, err),
				map[string]any{"jid": jid.String(), "error": err.Error()})
		}
	}
}

func (a *App) rewatchPresenceBounded() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	a.rewatchPresence(ctx)
}

func presenceStateFromEvent(jid types.JID, evt *events.Presence) PresenceState {
	state := PresenceState{JID: jid.String(), Online: !evt.Unavailable}
	if !evt.LastSeen.IsZero() {
		lastSeen := evt.LastSeen.UTC()
		state.LastSeen = &lastSeen
	}
	return state
}

// presenceEventData is a presence update as the sync reports it with --events,
// under the JID the store uses for the user's chat.
func (a *App) presenceEventData(ctx context.Context, evt *events.Presence) map[string]any {
	state := presenceStateFromEvent(a.canonicalStoreJID(ctx, evt.From), evt)
	data := map[string]any{"jid": state.JID, "online": state.Online}
	if state.LastSeen != nil {
		data["last_seen"] = state.LastSeen.Format(time.RFC3339)
	}
	return data
}

// chatPresenceEventData is a typing update as the sync reports it with
// --events: "composing" while someone types (media "audio" while they record a
// voice note) and "paused" when they stop.
func (a *App) chatPresenceEventData(ctx context.Context, evt *events.ChatPresence) (map[string]any, bool) {
	if evt == nil || evt.Chat.IsEmpty() {
		return nil, false
	}
	if evt.State != types.ChatPresenceComposing && evt.State != types.ChatPresencePaused {
		return nil, false
	}
	data := map[string]any{
		"chat_jid": a.canonicalStoreJID(ctx, evt.Chat).String(),
		"state":    string(evt.State),
		"media":    string(evt.Media),
	}
	if !evt.Sender.IsEmpty() {
		data["sender_jid"] = a.canonicalStoreJID(ctx, evt.Sender).String()
	}
	return data, true
}

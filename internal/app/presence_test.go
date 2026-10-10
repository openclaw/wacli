package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/out"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func TestWatchPresenceReturnsWhatsAppsAnswer(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	user := types.NewJID("15551234567", types.DefaultUserServer)
	lastSeen := time.Date(2026, 9, 28, 14, 32, 0, 0, time.FixedZone("CEST", 2*60*60))
	f.onSubscribePresence = func(jid types.JID) any {
		return &events.Presence{From: jid, Unavailable: true, LastSeen: lastSeen}
	}

	state, err := a.WatchPresence(context.Background(), user, time.Second)
	if err != nil {
		t.Fatalf("WatchPresence: %v", err)
	}
	if state == nil || state.JID != user.String() || state.Online {
		t.Fatalf("state = %+v, want %s offline", state, user)
	}
	if state.LastSeen == nil || !state.LastSeen.Equal(lastSeen) || state.LastSeen.Location() != time.UTC {
		t.Fatalf("last seen = %v, want %v in UTC", state.LastSeen, lastSeen)
	}
	if len(f.presenceSubscriptions) != 1 || f.presenceSubscriptions[0] != user {
		t.Fatalf("subscriptions = %v, want [%s]", f.presenceSubscriptions, user)
	}
}

// WhatsApp may answer a subscription made for a phone number from the user's
// LID; the answer is still theirs, and is reported under the phone number.
func TestWatchPresenceMatchesAnAnswerFromTheLID(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	pn := types.NewJID("15551234567", types.DefaultUserServer)
	lid := types.NewJID("99887766", types.HiddenUserServer)
	f.lids[lid] = pn
	f.onSubscribePresence = func(types.JID) any {
		return &events.Presence{From: lid}
	}

	state, err := a.WatchPresence(context.Background(), pn, time.Second)
	if err != nil {
		t.Fatalf("WatchPresence: %v", err)
	}
	if state == nil || state.JID != pn.String() || !state.Online || state.LastSeen != nil {
		t.Fatalf("state = %+v, want %s online", state, pn)
	}
}

func TestWatchPresenceIgnoresOtherUsers(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	other := types.NewJID("15550000000", types.DefaultUserServer)
	f.onSubscribePresence = func(types.JID) any {
		return &events.Presence{From: other}
	}

	state, err := a.WatchPresence(context.Background(), types.NewJID("15551234567", types.DefaultUserServer), 50*time.Millisecond)
	if err != nil || state != nil {
		t.Fatalf("WatchPresence = %+v, %v; want no answer", state, err)
	}
}

// A user who hides their presence gets no answer at all: that is not an error,
// and the subscription still stands for a later change.
func TestWatchPresenceWithoutAnAnswerIsNotAnError(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	user := types.NewJID("15551234567", types.DefaultUserServer)

	state, err := a.WatchPresence(context.Background(), user, 50*time.Millisecond)
	if err != nil || state != nil {
		t.Fatalf("WatchPresence = %+v, %v; want nil, nil", state, err)
	}
	if got := a.presenceWatch.list(); len(got) != 1 || got[0] != user {
		t.Fatalf("watched = %v, want [%s]", got, user)
	}
}

func TestWatchPresenceWithoutWaitReturnsAtOnce(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	f.onSubscribePresence = func(jid types.JID) any { return &events.Presence{From: jid} }

	state, err := a.WatchPresence(context.Background(), types.NewJID("15551234567", types.DefaultUserServer), 0)
	if err != nil || state != nil {
		t.Fatalf("WatchPresence = %+v, %v; want nil, nil", state, err)
	}
	if len(f.presenceSubscriptions) != 1 {
		t.Fatalf("subscriptions = %v, want one", f.presenceSubscriptions)
	}
}

func TestWatchPresenceRefusesAQuietSync(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	a.presenceWatch.setQuiet(true)

	_, err := a.WatchPresence(context.Background(), types.NewJID("15551234567", types.DefaultUserServer), time.Second)
	if !errors.Is(err, ErrPresenceQuiet) {
		t.Fatalf("WatchPresence error = %v, want ErrPresenceQuiet", err)
	}
	if len(f.presenceSubscriptions) != 0 || len(a.presenceWatch.list()) != 0 {
		t.Fatalf("a quiet sync subscribed anyway: sent %v, watched %v", f.presenceSubscriptions, a.presenceWatch.list())
	}
}

func TestWatchPresenceRejectsGroups(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	_, err := a.WatchPresence(context.Background(), types.NewJID("123456789", types.GroupServer), time.Second)
	if err == nil || !strings.Contains(err.Error(), "only be watched for a user") {
		t.Fatalf("WatchPresence error = %v, want a user-only error", err)
	}
	if len(f.presenceSubscriptions) != 0 {
		t.Fatalf("subscriptions = %v, want none", f.presenceSubscriptions)
	}
}

func TestWatchPresenceReportsASubscribeFailure(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	f.subscribePresenceErr = errors.New("not connected")

	_, err := a.WatchPresence(context.Background(), types.NewJID("15551234567", types.DefaultUserServer), time.Second)
	if err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("WatchPresence error = %v, want the subscribe failure", err)
	}
	if len(a.presenceWatch.list()) != 0 {
		t.Fatalf("a failed subscription was remembered: %v", a.presenceWatch.list())
	}
}

func TestPresenceWatchKeepsTheNewestSubscriptions(t *testing.T) {
	var w presenceWatch
	first := types.NewJID("1000", types.DefaultUserServer)
	w.add(first)
	for i := 0; i < maxWatchedPresences; i++ {
		w.add(types.NewJID(fmt.Sprintf("2%06d", i), types.DefaultUserServer))
	}
	got := w.list()
	if len(got) != maxWatchedPresences {
		t.Fatalf("watched %d users, want %d", len(got), maxWatchedPresences)
	}
	for _, jid := range got {
		if jid == first {
			t.Fatalf("the oldest subscription was kept past the limit")
		}
	}
	// Asking again for a user moves it to the newest end instead of adding it twice.
	again := got[0]
	w.add(again)
	got = w.list()
	if len(got) != maxWatchedPresences || got[len(got)-1] != again {
		t.Fatalf("re-watching %s: list has %d entries, last %s", again, len(got), got[len(got)-1])
	}
}

// WhatsApp forgets presence subscriptions with the connection that made them,
// so the sync asks again for every watched user once it has connected and
// shown itself available.
func TestSyncRenewsWatchedPresenceOnConnect(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	user := types.NewJID("15551234567", types.DefaultUserServer)
	a.presenceWatch.add(user)

	captureStderr(t, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		// The renewal runs beside the sync: leave it a moment before idling out.
		if _, err := a.Sync(ctx, SyncOptions{Mode: SyncModeOnce, IdleExit: 300 * time.Millisecond}); err != nil {
			t.Fatalf("Sync: %v", err)
		}
	})

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.presenceSubscriptions) != 1 || f.presenceSubscriptions[0] != user {
		t.Fatalf("subscriptions after connect = %v, want [%s]", f.presenceSubscriptions, user)
	}
}

// Renewing up to 256 subscriptions can take a while: it must not hold up the
// connection's event handling, and the sync's cleanup must still stop it.
func TestPresenceRenewalRunsOffTheEventPath(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	a.presenceWatch.add(types.NewJID("15551234567", types.DefaultUserServer))
	stalled := make(chan struct{})
	f.slowSubscribePresence = func(ctx context.Context, _ types.JID) error {
		close(stalled)
		<-ctx.Done() // WhatsApp never answers
		return ctx.Err()
	}

	returned := make(chan struct{})
	go func() {
		a.renewPresenceWatches(context.Background())
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("renewal held up its caller while a subscription stalled")
	}
	<-stalled

	stopped := make(chan struct{})
	go func() {
		a.presenceWatch.stopRenewal()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("stopping the renewal waited for the stalled subscription")
	}
}

// One contact that never answers costs the others nothing: each renewal has
// its own bound, and every contact of a full list is asked again.
func TestPresenceRenewalReachesEveryContactPastASlowOne(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	defer func(old time.Duration) { presenceRenewTimeout = old }(presenceRenewTimeout)
	presenceRenewTimeout = 20 * time.Millisecond
	slow := types.NewJID("1000", types.DefaultUserServer)
	a.presenceWatch.add(slow)
	for i := 1; i < maxWatchedPresences; i++ {
		a.presenceWatch.add(types.NewJID(fmt.Sprintf("2%06d", i), types.DefaultUserServer))
	}
	f.slowSubscribePresence = func(ctx context.Context, jid types.JID) error {
		if jid != slow {
			return nil
		}
		<-ctx.Done()
		return ctx.Err()
	}

	stderr := captureStderr(t, func() {
		a.renewPresenceWatches(context.Background())
		deadline := time.Now().Add(5 * time.Second)
		for {
			f.mu.Lock()
			done := len(f.presenceSubscribeAttempts) == maxWatchedPresences
			f.mu.Unlock()
			if done || time.Now().After(deadline) {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		a.presenceWatch.stopRenewal()
	})

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.presenceSubscribeAttempts) != maxWatchedPresences || f.presenceSubscribeAttempts[0] != slow {
		t.Fatalf("renewal asked %d contacts (first %v), want all %d starting with the slow one",
			len(f.presenceSubscribeAttempts), f.presenceSubscribeAttempts[:min(1, len(f.presenceSubscribeAttempts))], maxWatchedPresences)
	}
	if len(f.presenceSubscriptions) != maxWatchedPresences-1 {
		t.Fatalf("renewed %d subscriptions, want %d", len(f.presenceSubscriptions), maxWatchedPresences-1)
	}
	if !strings.Contains(stderr, "could not watch the presence of "+slow.String()+" again") {
		t.Fatalf("no warning for the contact that timed out; stderr = %q", stderr)
	}
}

func TestQuietSyncDoesNotRenewWatchedPresence(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	a.presenceWatch.add(types.NewJID("15551234567", types.DefaultUserServer))

	captureStderr(t, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err := a.Sync(ctx, SyncOptions{Mode: SyncModeOnce, IdleExit: time.Millisecond, PresenceMode: SyncPresenceModeQuiet}); err != nil {
			t.Fatalf("Sync: %v", err)
		}
	})

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.presenceSubscriptions) != 0 {
		t.Fatalf("a quiet sync renewed subscriptions: %v", f.presenceSubscriptions)
	}
}

func TestSyncReportsPresenceAndTypingAsEvents(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	pn := types.NewJID("15551234567", types.DefaultUserServer)
	lid := types.NewJID("99887766", types.HiddenUserServer)
	f.lids[lid] = pn
	group := types.NewJID("120363000000000000", types.GroupServer)
	lastSeen := time.Date(2026, 9, 28, 12, 30, 0, 0, time.UTC)

	var eventsOut bytes.Buffer
	a.opts.Events = out.NewEventWriter(&eventsOut, true)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := a.Sync(ctx, SyncOptions{
		Mode:     SyncModeOnce,
		IdleExit: time.Millisecond,
		AfterConnect: func(context.Context) error {
			f.emit(&events.Presence{From: lid, Unavailable: true, LastSeen: lastSeen})
			f.emit(&events.ChatPresence{
				MessageSource: types.MessageSource{Chat: lid, Sender: lid},
				State:         types.ChatPresenceComposing,
				Media:         types.ChatPresenceMediaAudio,
			})
			f.emit(&events.ChatPresence{
				MessageSource: types.MessageSource{Chat: group, Sender: pn, IsGroup: true},
				State:         types.ChatPresencePaused,
			})
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}

	got := map[string][]map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(eventsOut.String()), "\n") {
		var evt struct {
			Event string         `json:"event"`
			Data  map[string]any `json:"data"`
		}
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			t.Fatalf("decode event %q: %v", line, err)
		}
		got[evt.Event] = append(got[evt.Event], evt.Data)
	}
	presence := got["presence"]
	if len(presence) != 1 || presence[0]["jid"] != pn.String() || presence[0]["online"] != false || presence[0]["last_seen"] != "2026-09-28T12:30:00Z" {
		t.Fatalf("presence events = %v", presence)
	}
	typing := got["chat_presence"]
	if len(typing) != 2 {
		t.Fatalf("chat_presence events = %v, want 2", typing)
	}
	if typing[0]["chat_jid"] != pn.String() || typing[0]["sender_jid"] != pn.String() || typing[0]["state"] != "composing" || typing[0]["media"] != "audio" {
		t.Fatalf("direct chat typing event = %v", typing[0])
	}
	if typing[1]["chat_jid"] != group.String() || typing[1]["sender_jid"] != pn.String() || typing[1]["state"] != "paused" {
		t.Fatalf("group typing event = %v", typing[1])
	}
}

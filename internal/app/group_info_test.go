package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/wa"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

var (
	testGroupJID  = types.JID{User: "120363000000001", Server: types.GroupServer}
	testMemberJID = types.JID{User: "15550000001", Server: types.DefaultUserServer}
)

func testGroup(name string) *types.GroupInfo {
	return &types.GroupInfo{
		JID:          testGroupJID,
		GroupName:    types.GroupName{Name: name},
		Participants: []types.GroupParticipant{{JID: testMemberJID}},
	}
}

// useTestClock makes nowUTC return *at for the rest of the test.
func useTestClock(t *testing.T, at *time.Time) {
	t.Helper()
	previous := nowUTC
	nowUTC = func() time.Time { return *at }
	t.Cleanup(func() { nowUTC = previous })
}

func storeGroupText(t *testing.T, ctx context.Context, a *App, id string) {
	t.Helper()
	if err := a.storeParsedMessage(ctx, wa.ParsedMessage{
		Chat:      testGroupJID,
		ID:        id,
		SenderJID: testMemberJID.String(),
		PushName:  "Anna",
		Timestamp: time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC),
		Text:      "hello",
	}); err != nil {
		t.Fatalf("storeParsedMessage %s: %v", id, err)
	}
}

func assertGroupInfoCalls(t *testing.T, f *fakeWA, want int) {
	t.Helper()
	f.mu.Lock()
	got := f.groupInfoCalls
	f.mu.Unlock()
	if got != want {
		t.Fatalf("group info asked %d times, want %d", got, want)
	}
}

func assertGroupChatName(t *testing.T, a *App, want string) {
	t.Helper()
	c, err := a.db.GetChat(testGroupJID.String())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if c.Name != want {
		t.Fatalf("chat name = %q, want %q", c.Name, want)
	}
}

func storedParticipants(t *testing.T, a *App) int {
	t.Helper()
	ps, err := a.db.ListGroupParticipants(testGroupJID.String())
	if err != nil {
		t.Fatalf("ListGroupParticipants: %v", err)
	}
	return len(ps)
}

func TestHistorySyncAsksForGroupInfoOncePerGroup(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	f.groups[testGroupJID] = testGroup("Project")

	base := time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC)
	msgs := make([]*waHistorySync.HistorySyncMsg, 0, 200)
	for i := range 200 {
		msgs = append(msgs, &waHistorySync.HistorySyncMsg{Message: &waWeb.WebMessageInfo{
			Key: &waCommon.MessageKey{
				RemoteJID:   proto.String(testGroupJID.String()),
				FromMe:      proto.Bool(false),
				ID:          proto.String(fmt.Sprintf("group-history-%d", i)),
				Participant: proto.String(testMemberJID.String()),
			},
			MessageTimestamp: proto.Uint64(uint64(base.Add(time.Duration(i) * time.Minute).Unix())),
			Message:          &waProto.Message{Conversation: proto.String("hello")},
		}})
	}
	history := &events.HistorySync{Data: &waHistorySync.HistorySync{
		SyncType: waHistorySync.HistorySync_FULL.Enum(),
		Conversations: []*waHistorySync.Conversation{{
			ID:       proto.String(testGroupJID.String()),
			Messages: msgs,
		}},
	}}

	var messagesStored atomic.Int64
	var lastEvent atomic.Int64
	a.handleHistorySync(context.Background(), SyncOptions{}, history, &messagesStored, &lastEvent, func(string, string) {})

	if got := messagesStored.Load(); got != 200 {
		t.Fatalf("stored %d messages, want 200", got)
	}
	assertGroupInfoCalls(t, f, 1)
	assertGroupChatName(t, a, "Project")
	if n := storedParticipants(t, a); n != 1 {
		t.Fatalf("stored participants = %d, want 1", n)
	}
}

func TestGroupInfoIsReusedAndStoredOnceUntilItExpires(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	useTestClock(t, &now)
	ctx := context.Background()
	f.groups[testGroupJID] = testGroup("Project")

	storeGroupText(t, ctx, a, "m1")
	assertGroupInfoCalls(t, f, 1)
	assertGroupChatName(t, a, "Project")
	if n := storedParticipants(t, a); n != 1 {
		t.Fatalf("stored participants = %d, want 1", n)
	}

	// Within the reuse window neither the servers nor the stored snapshot are
	// touched: the participants cleared here stay cleared.
	f.groups[testGroupJID] = testGroup("Renamed")
	if err := a.db.ReplaceGroupParticipants(testGroupJID.String(), nil); err != nil {
		t.Fatalf("ReplaceGroupParticipants: %v", err)
	}
	now = now.Add(groupInfoReuse - time.Second)
	storeGroupText(t, ctx, a, "m2")
	assertGroupInfoCalls(t, f, 1)
	assertGroupChatName(t, a, "Project")
	if n := storedParticipants(t, a); n != 0 {
		t.Fatalf("snapshot rewritten from a reused answer: %d participants", n)
	}

	// Past it the servers are asked again and the new answer is stored.
	now = now.Add(2 * time.Second)
	storeGroupText(t, ctx, a, "m3")
	assertGroupInfoCalls(t, f, 2)
	assertGroupChatName(t, a, "Renamed")
	if n := storedParticipants(t, a); n != 1 {
		t.Fatalf("stored participants after a new answer = %d, want 1", n)
	}
}

func TestFailedGroupInfoLookupIsReusedBriefly(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	useTestClock(t, &now)
	ctx := context.Background()
	f.groupInfoErr = errors.New("not in group")

	for i := range 5 {
		storeGroupText(t, ctx, a, fmt.Sprintf("m%d", i))
	}
	assertGroupInfoCalls(t, f, 1)
	// Without the group's info the chat is named as ResolveChatName would.
	assertGroupChatName(t, a, "Anna")

	f.mu.Lock()
	f.groupInfoErr = nil
	f.mu.Unlock()
	f.groups[testGroupJID] = testGroup("Project")
	now = now.Add(groupInfoFailureReuse + time.Second)
	storeGroupText(t, ctx, a, "m-after")
	assertGroupInfoCalls(t, f, 2)
	assertGroupChatName(t, a, "Project")
}

func TestGroupChangeEventsAskForGroupInfoAgain(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	ctx := context.Background()
	f.groups[testGroupJID] = testGroup("Project")

	var messagesStored atomic.Int64
	var lastEvent atomic.Int64
	handlerID, _ := a.addSyncEventHandler(
		ctx,
		SyncOptions{Mode: SyncModeFollow},
		&messagesStored,
		&lastEvent,
		make(chan struct{}, 1),
		make(chan struct{}, 1),
		make(chan staleReconnectRequest, 1),
		func(string, string) {},
		nil,
		nil,
		&syncPresence{},
		nil,
	)
	defer f.RemoveEventHandler(handlerID)

	storeGroupText(t, ctx, a, "m1")
	assertGroupInfoCalls(t, f, 1)

	f.groups[testGroupJID] = testGroup("Renamed")
	f.emit(&events.GroupInfo{JID: testGroupJID, Name: &types.GroupName{Name: "Renamed"}})
	storeGroupText(t, ctx, a, "m2")
	assertGroupInfoCalls(t, f, 2)
	assertGroupChatName(t, a, "Renamed")

	f.emit(&events.JoinedGroup{GroupInfo: types.GroupInfo{JID: testGroupJID}})
	storeGroupText(t, ctx, a, "m3")
	assertGroupInfoCalls(t, f, 3)

	// A reconnect drops every kept answer, so nothing asked before a
	// disconnect is reused after it.
	storeGroupText(t, ctx, a, "m4")
	assertGroupInfoCalls(t, f, 3)
	f.emit(&events.Connected{})
	storeGroupText(t, ctx, a, "m5")
	assertGroupInfoCalls(t, f, 4)
}

func TestGroupInfoAskedAcrossAChangeIsRefetched(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	ctx := context.Background()
	f.groups[testGroupJID] = testGroup("Project")

	// The first answer is invalidated in flight and must be fetched again.
	f.onGroupInfo = func() {
		f.mu.Lock()
		f.onGroupInfo = nil
		f.mu.Unlock()
		a.forgetGroupInfo(testGroupJID)
	}
	storeGroupText(t, ctx, a, "m1")
	assertGroupInfoCalls(t, f, 2)

	f.mu.Lock()
	f.onGroupInfo = nil
	f.mu.Unlock()
	storeGroupText(t, ctx, a, "m2")
	assertGroupInfoCalls(t, f, 2)
	storeGroupText(t, ctx, a, "m3")
	assertGroupInfoCalls(t, f, 2)
}

// renameTable moves a table of the app's store out of the way, or back.
func renameTable(t *testing.T, a *App, from, to string) {
	t.Helper()
	raw, err := sql.Open("sqlite3", filepath.Join(a.opts.StoreDir, "wacli.db"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer raw.Close()
	if _, err := raw.Exec(fmt.Sprintf(`ALTER TABLE %q RENAME TO %q`, from, to)); err != nil {
		t.Fatalf("rename %s to %s: %v", from, to, err)
	}
}

func TestGroupSnapshotIsRetriedAfterAFailedWrite(t *testing.T) {
	for _, tc := range []struct {
		name    string
		table   string // made to fail while the first message is stored
		wantErr bool
	}{
		{name: "snapshot write fails", table: "groups"},
		{name: "chat write fails before the snapshot", table: "chats", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestApp(t)
			f := newFakeWA()
			a.wa = f
			ctx := context.Background()
			f.groups[testGroupJID] = testGroup("Project")

			renameTable(t, a, tc.table, tc.table+"_away")
			err := a.storeParsedMessage(ctx, wa.ParsedMessage{
				Chat:      testGroupJID,
				ID:        "m1",
				SenderJID: testMemberJID.String(),
				PushName:  "Anna",
				Timestamp: time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC),
				Text:      "hello",
			})
			if (err != nil) != tc.wantErr {
				t.Fatalf("storeParsedMessage error = %v, want error %v", err, tc.wantErr)
			}
			renameTable(t, a, tc.table+"_away", tc.table)
			if n := storedParticipants(t, a); n != 0 {
				t.Fatalf("participants stored while the write failed: %d", n)
			}

			// The next message stores the snapshot from the kept answer.
			storeGroupText(t, ctx, a, "m2")
			assertGroupInfoCalls(t, f, 1)
			if n := storedParticipants(t, a); n != 1 {
				t.Fatalf("snapshot not retried after the failed write: %d participants", n)
			}

			// Once stored, it is not written again for that answer.
			if err := a.db.ReplaceGroupParticipants(testGroupJID.String(), nil); err != nil {
				t.Fatalf("ReplaceGroupParticipants: %v", err)
			}
			storeGroupText(t, ctx, a, "m3")
			assertGroupInfoCalls(t, f, 1)
			if n := storedParticipants(t, a); n != 0 {
				t.Fatalf("snapshot written again after it was stored: %d participants", n)
			}
		})
	}
}

func TestGroupInfoAskedWhileStoppingIsNotKept(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	f.groups[testGroupJID] = testGroup("Project")

	stopping, cancel := context.WithCancel(context.Background())
	cancel()
	storeGroupText(t, stopping, a, "m1")
	assertGroupInfoCalls(t, f, 1)

	storeGroupText(t, context.Background(), a, "m2")
	assertGroupInfoCalls(t, f, 2)
}

type delayedGroupAnswerWA struct {
	*fakeWA
	first   atomic.Bool
	ready   chan struct{}
	release chan struct{}
}

func (f *delayedGroupAnswerWA) GetGroupInfo(ctx context.Context, jid types.JID) (*types.GroupInfo, error) {
	info, err := f.fakeWA.GetGroupInfo(ctx, jid)
	if f.first.CompareAndSwap(false, true) {
		close(f.ready)
		<-f.release
	}
	return info, err
}
func TestGroupInfoInvalidatedLookupCannotOverwriteNewerSnapshot(t *testing.T) {
	a := newTestApp(t)
	f := &delayedGroupAnswerWA{fakeWA: newFakeWA(), ready: make(chan struct{}), release: make(chan struct{})}
	a.wa = f
	f.groups[testGroupJID] = testGroup("Old")
	done := make(chan error, 1)
	finished := false
	t.Cleanup(func() {
		select {
		case <-f.release:
		default:
			close(f.release)
		}
		if !finished {
			<-done
		}
	})
	go func() {
		done <- a.storeParsedMessage(t.Context(), wa.ParsedMessage{Chat: testGroupJID, ID: "old", Timestamp: time.Now(), Text: "old"})
	}()
	<-f.ready
	a.forgetGroupInfo(testGroupJID)
	newer := testGroup("New")
	newer.Participants = append(newer.Participants, types.GroupParticipant{JID: types.NewJID("15550000002", types.DefaultUserServer)})
	f.mu.Lock()
	f.groups[testGroupJID] = newer
	f.mu.Unlock()
	storeGroupText(t, t.Context(), a, "new")
	close(f.release)
	err := <-done
	finished = true
	if err != nil {
		t.Fatal(err)
	}
	if got := storedParticipants(t, a); got != 2 {
		t.Fatalf("stale lookup overwrote newer snapshot: participants=%d,want2", got)
	}
	assertGroupChatName(t, a, "New")
}

func TestGroupInfoOtherGroupChangeDoesNotDiscardSnapshot(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	f.groups[testGroupJID] = testGroup("Project")
	f.onGroupInfo = func() { a.forgetGroupInfo(types.NewJID("120363000000002", types.GroupServer)) }
	storeGroupText(t, t.Context(), a, "m1")
	if got := storedParticipants(t, a); got != 1 {
		t.Fatalf("unrelated invalidation discarded snapshot: participants=%d,want1", got)
	}
}

func TestGroupInfoConcurrentMissesShareLookup(t *testing.T) {
	a := newTestApp(t)
	f := &delayedGroupAnswerWA{fakeWA: newFakeWA(), ready: make(chan struct{}), release: make(chan struct{})}
	a.wa = f
	f.groups[testGroupJID] = testGroup("Project")
	done := make(chan struct{}, 2)
	go func() { a.cachedGroupInfo(t.Context(), testGroupJID); done <- struct{}{} }()
	<-f.ready
	go func() { a.cachedGroupInfo(t.Context(), testGroupJID); done <- struct{}{} }()
	select {
	case <-done:
		done <- struct{}{}
	case <-time.After(100 * time.Millisecond):
	}
	close(f.release)
	<-done
	<-done
	assertGroupInfoCalls(t, f.fakeWA, 1)
}

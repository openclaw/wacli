//go:build !windows

package main

import (
	"bytes"
	"compress/zlib"
	"database/sql"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/sqliteutil"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// A command that connected by itself must exit within its own deadline even
// when the store is still busy with an incoming event at exit. Here the
// event is a history chunk from the phone (inline, as whatsmeow delivers the
// first bootstrap chunk), delivered right after login; the test holds
// wacli.db's write lock from that moment, so the import sits in SQLite's busy
// wait (5 s), which the command's deadline cannot cut. Through the real
// `chats archive` command: it connects, fails to resolve the chat, and runs
// its cleanup (the deferred Disconnect and removal of its app-state
// persistence handler, then App.Close). whatsmeow's RemoveEventHandler waits
// for the event being dispatched, so a removal before the bounded wait at
// exit would keep the command until the store returns.
func TestOneShotChatsCleanupExitsWithinItsDeadlineRealTransport(t *testing.T) {
	env := newBacklogEnv(t)
	env.peer.answerAtOnce = true
	env.peer.deliverFirst = 300 * time.Millisecond
	own := *env.device.ID
	own.Device = 0
	at := time.Unix(1700000000, 0).UTC()
	env.peer.hold(&backlogItem{id: "hist-1", class: "message", node: env.sender(own)("hist-1", own.ToNonAD(), at, backlogHistoryNotification(t, env.contact, at, "hist-msg-1"))})

	locker, err := sql.Open("sqlite3", sqliteutil.FileURI(filepath.Join(env.dir, "wacli.db"), "_busy_timeout=5000"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = locker.Close() })
	var lockMu sync.Mutex
	var tx *sql.Tx
	release := func() {
		lockMu.Lock()
		defer lockMu.Unlock()
		if tx != nil {
			_ = tx.Rollback()
			tx = nil
		}
	}
	t.Cleanup(release)
	env.peer.beforeDeliver = func() {
		lockMu.Lock()
		defer lockMu.Unlock()
		var err error
		if tx, err = locker.BeginTx(env.ctx, nil); err == nil {
			_, err = tx.Exec(`INSERT INTO chats(jid, kind, name, last_message_ts) VALUES ('locker@s.whatsapp.net', 'dm', 'locker', 0)`)
		}
		if err != nil {
			t.Errorf("hold wacli.db's write lock: %v", err)
			return
		}
		t.Log("TEST holds wacli.db's write lock: the store's next write waits in SQLite's busy wait (5 s)")
	}

	const deadline = 2 * time.Second
	stacks := make(chan string, 1)
	watchdog := time.AfterFunc(deadline+1500*time.Millisecond, func() {
		buf := make([]byte, 1<<20)
		stacks <- string(buf[:runtime.Stack(buf, true)])
	})
	defer watchdog.Stop()
	start := time.Now()
	_, stderr, err := env.cli("one-shot chats archive while a history chunk is being stored", "--timeout", deadline.String(), "chats", "archive", "--chat", "nobody by this name")
	took := time.Since(start)
	t.Logf("CHECK the command returned after %s (deadline %s): err=%v", took.Round(time.Millisecond), deadline, err)
	if err == nil {
		t.Fatal("chats archive resolved a chat that does not exist")
	}
	select {
	case dump := <-stacks:
		var blocked []string
		for _, g := range strings.Split(dump, "\n\n") {
			if strings.Contains(g, "RemoveEventHandler") || strings.Contains(g, "main.runChatState") || strings.Contains(g, "(*App).Close") {
				blocked = append(blocked, g)
			}
		}
		t.Logf("the command was still running %s after it started; where its cleanup was:\n%s", deadline+1500*time.Millisecond, strings.Join(blocked, "\n\n"))
	default:
	}
	if took > deadline+time.Second {
		t.Fatalf("the command took %s, past its %s deadline: a handler removal on its cleanup path waited for the event the store was handling", took.Round(time.Millisecond), deadline)
	}
	if !strings.Contains(stderr, "oneshot_store_incomplete") && !strings.Contains(stderr, "still being stored") {
		t.Fatalf("no warning for the history chunk cut short at exit; stderr:\n%s", stderr)
	}
}

// backlogHistoryNotification is the protocol message the phone sends with a
// history chunk: here one message in the contact's chat, carried inline.
func backlogHistoryNotification(t *testing.T, chat types.JID, at time.Time, msgID string) *waE2E.Message {
	t.Helper()
	chunk, err := proto.Marshal(&waHistorySync.HistorySync{
		SyncType: waHistorySync.HistorySync_RECENT.Enum(),
		Conversations: []*waHistorySync.Conversation{{
			ID: proto.String(chat.String()),
			Messages: []*waHistorySync.HistorySyncMsg{{Message: &waWeb.WebMessageInfo{
				Key:              &waCommon.MessageKey{RemoteJID: proto.String(chat.String()), FromMe: proto.Bool(false), ID: proto.String(msgID)},
				MessageTimestamp: proto.Uint64(uint64(at.Unix())),
				Message:          &waE2E.Message{Conversation: proto.String("from the history chunk")},
			}}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var packed bytes.Buffer
	w := zlib.NewWriter(&packed)
	if _, err := w.Write(chunk); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	syncType := waE2E.HistorySyncType_RECENT
	return &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type: waE2E.ProtocolMessage_HISTORY_SYNC_NOTIFICATION.Enum(),
		HistorySyncNotification: &waE2E.HistorySyncNotification{
			SyncType:                          &syncType,
			InitialHistBootstrapInlinePayload: packed.Bytes(),
		},
	}}
}

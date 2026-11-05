package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/delegate"
	"github.com/openclaw/wacli/internal/lock"
	"go.mau.fi/whatsmeow/types"
)

type fakeDeleteChatCall struct {
	chat    types.JID
	through time.Time
}

// fakeDeleteChatApp answers the way app.DeleteChat does: a range it sent, or
// a refusal or failure set by the test.
type fakeDeleteChatApp struct {
	calls   chan fakeDeleteChatCall
	deleted app.ChatDeleted
	err     error
}

func (f *fakeDeleteChatApp) DeleteChat(_ context.Context, chat types.JID, through time.Time) (app.ChatDeleted, error) {
	f.calls <- fakeDeleteChatCall{chat: chat, through: through}
	if f.err != nil {
		return app.ChatDeleted{}, f.err
	}
	deleted := f.deleted
	deleted.Chat = chat
	return deleted, nil
}

func newFakeDeleteChatApp() *fakeDeleteChatApp {
	return &fakeDeleteChatApp{
		calls: make(chan fakeDeleteChatCall, 4),
		deleted: app.ChatDeleted{
			LastMessageID: "3EB0AAAA0000000000001",
			Through:       time.Date(2026, 3, 4, 9, 59, 12, 0, time.UTC),
		},
	}
}

func TestExecuteDelegatedDeleteChatPassesTheChatAndTheTime(t *testing.T) {
	fake := newFakeDeleteChatApp()
	resp, err := executeDelegatedDeleteChat(context.Background(), fake, sendDelegateRequest{
		Kind:    deleteChatKind,
		To:      "15550001010@s.whatsapp.net",
		Through: "2026-03-04T10:00:00.250Z",
	})
	if err != nil {
		t.Fatalf("executeDelegatedDeleteChat: %v", err)
	}
	call := <-fake.calls
	if call.chat.String() != "15550001010@s.whatsapp.net" || !call.through.Equal(time.Date(2026, 3, 4, 10, 0, 0, 250_000_000, time.UTC)) {
		t.Fatalf("call = %+v, want the chat through 10:00:00.250", call)
	}
	if !resp.OK || resp.Code != "deleted" || resp.Chat != "15550001010@s.whatsapp.net" ||
		resp.LastMessageID != "3EB0AAAA0000000000001" || resp.DeletedThrough != "2026-03-04T09:59:12Z" || resp.ID != "" {
		t.Fatalf("response = %+v", resp)
	}
}

func TestExecuteDelegatedDeleteChatValidatesBeforeAppUse(t *testing.T) {
	tests := []struct {
		name, to, through, want string
	}{
		{name: "no chat", through: "2026-03-04T10:00:00Z", want: "--chat is required"},
		{name: "a name", to: "Nora Vitale", through: "2026-03-04T10:00:00Z", want: "--chat must be a JID or a phone number"},
		{name: "no time", to: "15550001010@s.whatsapp.net", want: "--through is required"},
		{name: "not RFC 3339", to: "15550001010@s.whatsapp.net", through: "1772618400", want: "--through must be an RFC 3339 time"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeDeleteChatApp()
			_, err := executeDelegatedDeleteChat(context.Background(), fake, sendDelegateRequest{Kind: deleteChatKind, To: tt.to, Through: tt.through})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
			select {
			case call := <-fake.calls:
				t.Fatalf("invalid request reached the app: %+v", call)
			default:
			}
		})
	}
}

func TestExecuteDelegatedSendRoutesDeleteChat(t *testing.T) {
	a, err := app.New(app.Options{StoreDir: t.TempDir()})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	t.Cleanup(a.Close)

	_, err = executeDelegatedSend(context.Background(), a, sendDelegateRequest{
		Version: sendDelegateVersion,
		Kind:    deleteChatKind,
		To:      "15550001010@s.whatsapp.net",
	})
	if err == nil || !strings.Contains(err.Error(), "--through is required") {
		t.Fatalf("error = %v, want delete_chat's own validation", err)
	}
}

// TestDeleteChatSocketContract holds the delegate socket's request and answer
// as a client such as Postmaster writes and reads them, byte for byte JSON.
func TestDeleteChatSocketContract(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	refusal := func(code, reason string) error { return &app.ChatDeleteRefusal{Code: code, Reason: reason} }
	tests := []struct {
		name string
		err  error
		want map[string]any
	}{
		{name: "deleted", want: map[string]any{
			"ok": true, "code": "deleted", "chat": "15550001010@s.whatsapp.net",
			"last_message_id": "3EB0AAAA0000000000001", "deleted_through": "2026-03-04T09:59:12Z",
		}},
		{name: "newer messages", err: refusal(app.ChatDeleteNewerMessages, "1 stored message(s) are later"), want: map[string]any{
			"ok": false, "code": "newer_messages", "error": "newer_messages: 1 stored message(s) are later",
		}},
		{name: "not found", err: refusal(app.ChatDeleteNotFound, "no chat is stored"), want: map[string]any{
			"ok": false, "code": "not_found", "error": "not_found: no chat is stored",
		}},
		{name: "not one to one", err: fmt.Errorf("delete: %w", refusal(app.ChatDeleteNotOneToOne, "a group")), want: map[string]any{
			"ok": false, "code": "not_one_to_one", "error": "delete: not_one_to_one: a group",
		}},
		{name: "failure", err: fmt.Errorf("server rejected patch"), want: map[string]any{
			"ok": false, "error": "server rejected patch",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			storeDir := shortPresenceDelegateStoreDir(t)
			fake := newFakeDeleteChatApp()
			fake.err = tt.err
			stop, err := startSendDelegateServerForStore(context.Background(), storeDir, sendSpacing{}, func(ctx context.Context, req sendDelegateRequest) (sendDelegateResponse, error) {
				if req.Version != sendDelegateVersion || req.Kind != deleteChatKind {
					return sendDelegateResponse{}, fmt.Errorf("unexpected delegated request %d/%q", req.Version, req.Kind)
				}
				return executeDelegatedDeleteChat(ctx, fake, req)
			})
			if err != nil {
				t.Fatalf("start delegate server: %v", err)
			}
			defer stop()

			conn, err := delegate.Dial(contextWithTestTimeout(t), storeDir)
			if err != nil {
				t.Fatalf("dial delegate socket: %v", err)
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
			request := `{"version":1,"kind":"delete_chat","to":"15550001010@s.whatsapp.net","through":"2026-03-04T10:00:00Z","timeout_ms":5000}` + "\n"
			if _, err := conn.Write([]byte(request)); err != nil {
				t.Fatalf("write request: %v", err)
			}
			line, err := bufio.NewReader(conn).ReadBytes('\n')
			if err != nil {
				t.Fatalf("read answer: %v", err)
			}
			var got map[string]any
			if err := json.Unmarshal(line, &got); err != nil {
				t.Fatalf("answer %q is no JSON object: %v", line, err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("answer = %s, want exactly %v", line, tt.want)
			}
			for key, want := range tt.want {
				if got[key] != want {
					t.Fatalf("answer = %s: %s = %v, want %v", line, key, got[key], want)
				}
			}
			call := <-fake.calls
			if call.chat.String() != "15550001010@s.whatsapp.net" || !call.through.Equal(time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)) {
				t.Fatalf("call = %+v", call)
			}
		})
	}
}

func TestDelegatedDeleteChatDoesNotWaitForSendQueue(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	if err := clientConn.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set client deadline: %v", err)
	}
	sendSlot := make(chan struct{}, 1) // empty means an earlier send owns it
	fake := newFakeDeleteChatApp()
	go handleSendDelegateConn(context.Background(), serverConn, func(ctx context.Context, req sendDelegateRequest) (sendDelegateResponse, error) {
		return executeDelegatedDeleteChat(ctx, fake, req)
	}, sendSlot, newSendPacer(sendSpacing{}))

	if err := json.NewEncoder(clientConn).Encode(sendDelegateRequest{
		Version: sendDelegateVersion,
		Kind:    deleteChatKind,
		To:      "15550001010@s.whatsapp.net",
		Through: "2026-03-04T10:00:00Z",
	}); err != nil {
		t.Fatalf("encode request: %v", err)
	}
	var resp sendDelegateResponse
	if err := json.NewDecoder(clientConn).Decode(&resp); err != nil {
		t.Fatalf("delete waited behind the send queue: %v", err)
	}
	if !resp.OK || resp.Code != "deleted" {
		t.Fatalf("response = %+v, want deleted", resp)
	}
}

func TestChatsDeleteDelegatesThroughProductionServerWhenStoreLocked(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	storeDir := shortPresenceDelegateStoreDir(t)
	lk, err := lock.Acquire(storeDir)
	if err != nil {
		t.Fatalf("lock store: %v", err)
	}
	defer lk.Release()

	fake := newFakeDeleteChatApp()
	stop, err := startSendDelegateServerForStore(context.Background(), storeDir, sendSpacing{}, func(ctx context.Context, req sendDelegateRequest) (sendDelegateResponse, error) {
		if req.Version != sendDelegateVersion || req.Kind != deleteChatKind {
			return sendDelegateResponse{}, fmt.Errorf("unexpected delegated request %d/%q", req.Version, req.Kind)
		}
		return executeDelegatedDeleteChat(ctx, fake, req)
	})
	if err != nil {
		t.Fatalf("start production delegate server: %v", err)
	}
	defer stop()

	args := []string{"--store", storeDir, "--json", "--timeout", "750ms", "chats", "delete", "--chat", "+1 555 000 1010", "--through", "2026-03-04T11:00:00+01:00"}
	stdout, stderr, err := runPresenceDelegateHelper(t, args)
	if err != nil {
		t.Fatalf("chats delete failed: %v stdout=%q stderr=%q", err, stdout, stderr)
	}
	select {
	case call := <-fake.calls:
		if call.chat.String() != "15550001010@s.whatsapp.net" || !call.through.Equal(time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)) {
			t.Fatalf("delegated call = %+v", call)
		}
	case <-contextWithTestTimeout(t).Done():
		t.Fatal("timed out waiting for the delegated delete")
	}
	for _, want := range []string{`"ok":true`, `"action":"delete"`, `"code":"deleted"`, `"chat":"15550001010@s.whatsapp.net"`, `"last_message_id":"3EB0AAAA0000000000001"`, `"deleted_through":"2026-03-04T09:59:12Z"`} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout %q missing %s", stdout, want)
		}
	}

	fake.err = &app.ChatDeleteRefusal{Code: app.ChatDeleteNewerMessages, Reason: "2 stored message(s) are later"}
	stdout, stderr, err = runPresenceDelegateHelper(t, args)
	if err == nil {
		t.Fatalf("refused delete succeeded: stdout=%q", stdout)
	}
	<-fake.calls
	if !strings.Contains(stderr, "newer_messages: 2 stored message(s) are later") {
		t.Fatalf("stderr %q does not carry the refusal", stderr)
	}
}

func TestChatsDeleteValidatesBeforeOpeningTheStore(t *testing.T) {
	storeDir := t.TempDir()
	tests := []struct {
		args []string
		want string
	}{
		{args: []string{"--through", "2026-03-04T10:00:00Z"}, want: "--chat is required"},
		{args: []string{"--chat", "15550001010@s.whatsapp.net"}, want: "--through is required"},
		{args: []string{"--chat", "15550001010@s.whatsapp.net", "--through", "yesterday"}, want: "--through must be an RFC 3339 time"},
	}
	for _, tt := range tests {
		err := execute(append([]string{"--store", storeDir, "chats", "delete"}, tt.args...))
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Fatalf("chats delete %v: error = %v, want %q", tt.args, err, tt.want)
		}
	}
}

func TestExplainDeleteChatDelegateErrorNamesTheRestart(t *testing.T) {
	rejected := fmt.Errorf(`unsupported send kind %q`, deleteChatKind)
	err := explainDeleteChatDelegateError(rejected)
	if err == nil || !strings.Contains(err.Error(), "restart `wacli sync`") || !strings.Contains(err.Error(), "deleted nothing") {
		t.Fatalf("error = %v, want the restart hint", err)
	}
	other := fmt.Errorf("server rejected patch")
	if got := explainDeleteChatDelegateError(other); got != other {
		t.Fatalf("other error rewritten: %v", got)
	}
}

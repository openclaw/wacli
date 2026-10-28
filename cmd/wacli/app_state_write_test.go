package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/lock"
	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow/types"
)

type fakeAppStateWriteApp struct {
	calls       chan string
	patchedChat string
}

func (f *fakeAppStateWriteApp) DB() *store.DB { return nil }

func (f *fakeAppStateWriteApp) record(format string, args ...any) {
	f.calls <- fmt.Sprintf(format, args...)
}

func deref[T any](v *T) any {
	if v == nil {
		return "<nil>"
	}
	return *v
}

func (f *fakeAppStateWriteApp) CreateLabel(_ context.Context, in app.LabelInput) (store.List, error) {
	f.record("create id=%q name=%v color=%v", in.ID, deref(in.Name), deref(in.Color))
	return store.List{ID: "10", Name: *in.Name}, nil
}

func (f *fakeAppStateWriteApp) EditLabel(_ context.Context, ref string, in app.LabelInput) (store.List, error) {
	f.record("edit %s name=%v color=%v", ref, deref(in.Name), deref(in.Color))
	return store.List{ID: "10", Name: "x"}, nil
}

func (f *fakeAppStateWriteApp) DeleteLabel(_ context.Context, ref string) (store.List, error) {
	f.record("delete %s", ref)
	return store.List{ID: ref}, nil
}

func (f *fakeAppStateWriteApp) SetChatLabel(_ context.Context, chat types.JID, ref string, labeled bool) (app.ChatLabelResult, error) {
	f.record("label %s %s %t", chat, ref, labeled)
	target := chat.String()
	if f.patchedChat != "" {
		target = f.patchedChat
	}
	return app.ChatLabelResult{Label: store.List{ID: ref}, Chat: target}, nil
}

func (f *fakeAppStateWriteApp) ResyncAppState(_ context.Context, collection string) (app.AppStateResyncResult, error) {
	f.record("resync %s", collection)
	return app.AppStateResyncResult{Collection: collection, Completeness: "complete", QuickReplies: 2, Lists: 1}, nil
}

func TestChatLabelResultReportsPatchedJID(t *testing.T) {
	f := &fakeAppStateWriteApp{calls: make(chan string, 1), patchedChat: "123@s.whatsapp.net"}
	result, err := executeAppStateWrite(t.Context(), f, appStateWriteRequest{Action: chatLabelAddAction, Ref: "7", Chat: "999@lid"})
	if err != nil || result.Chat != "123@s.whatsapp.net" || result.Label == nil || result.Label.ID != "7" {
		t.Fatalf("result = %+v, %v", result, err)
	}
}

func (f *fakeAppStateWriteApp) CreateQuickReply(_ context.Context, in app.QuickReplyInput) (store.QuickReply, error) {
	f.record("qr create %v %v %v", deref(in.Shortcut), deref(in.Message), deref(in.Keywords))
	return store.QuickReply{ID: "1", Shortcut: *in.Shortcut}, nil
}

func (f *fakeAppStateWriteApp) EditQuickReply(_ context.Context, ref string, in app.QuickReplyInput) (store.QuickReply, error) {
	f.record("qr edit %s %v %v %v", ref, deref(in.Shortcut), deref(in.Message), deref(in.Keywords))
	return store.QuickReply{ID: "1"}, nil
}

func (f *fakeAppStateWriteApp) DeleteQuickReply(_ context.Context, ref string) (store.QuickReply, error) {
	f.record("qr delete %s", ref)
	return store.QuickReply{ID: ref, Deleted: true}, nil
}

func TestAppStateWritesThroughProductionServerWhenStoreLocked(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	storeDir := shortPresenceDelegateStoreDir(t)
	lk, err := lock.Acquire(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	defer lk.Release()
	fake := &fakeAppStateWriteApp{calls: make(chan string, 1)}
	stop, err := startSendDelegateServerForStore(context.Background(), storeDir, sendSpacing{}, func(ctx context.Context, req sendDelegateRequest) (sendDelegateResponse, error) {
		if req.Kind == appStateResyncKind {
			return executeDelegatedAppStateResync(ctx, fake, req)
		}
		if req.Kind != appStateWriteKind {
			return sendDelegateResponse{}, fmt.Errorf("unexpected kind %q", req.Kind)
		}
		return executeDelegatedAppStateWrite(ctx, fake, req)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"lists", "create", "--name", "Prueba Vendas"}, `create id="" name=Prueba Vendas color=<nil>`},
		{[]string{"labels", "edit", "10", "--name", "Otra", "--color", "2"}, "edit 10 name=Otra color=2"},
		{[]string{"lists", "delete", "Otra"}, "delete Otra"},
		{[]string{"chats", "label", "add", "--chat", "123@s.whatsapp.net", "--label", "10"}, "label 123@s.whatsapp.net 10 true"},
		{[]string{"chats", "label", "remove", "--chat", "15550000001@s.whatsapp.net", "--label", "10"}, "label 15550000001@s.whatsapp.net 10 false"},
		{[]string{"quick-replies", "create", "--shortcut", "prueba", "--message", "Hola"}, "qr create prueba Hola <nil>"},
		{[]string{"quick-replies", "edit", "prueba", "--keyword", "k"}, "qr edit prueba <nil> <nil> [k]"},
		{[]string{"quick-replies", "delete", "prueba"}, "qr delete prueba"},
		{[]string{"app-state", "resync", "regular"}, "resync regular"},
	} {
		stdout, stderr, err := runPresenceDelegateHelper(t, append([]string{"--store", storeDir, "--timeout", "5s"}, tt.args...))
		if err != nil {
			t.Fatalf("%v: %v stdout=%q stderr=%q", tt.args, err, stdout, stderr)
		}
		select {
		case got := <-fake.calls:
			if got != tt.want {
				t.Fatalf("%v: call = %q, want %q", tt.args, got, tt.want)
			}
		case <-contextWithTestTimeout(t).Done():
			t.Fatalf("%v: not delegated", tt.args)
		}
		if !strings.Contains(stdout, ": ") {
			t.Fatalf("%v: human output = %q", tt.args, stdout)
		}
	}
}

func TestAppStateWriteOldOwnerRejectionNamesRestart(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	storeDir := shortPresenceDelegateStoreDir(t)
	lk, err := lock.Acquire(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	defer lk.Release()
	// An owner built before this feature rejects the unknown kind.
	stop, err := startSendDelegateServerForStore(context.Background(), storeDir, sendSpacing{}, func(_ context.Context, req sendDelegateRequest) (sendDelegateResponse, error) {
		return sendDelegateResponse{}, fmt.Errorf("unsupported send kind %q", req.Kind)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	for _, args := range [][]string{{"lists", "create", "--name", "x"}, {"app-state", "resync", "regular"}} {
		stdout, stderr, err := runPresenceDelegateHelper(t, append([]string{"--store", storeDir, "--timeout", "5s"}, args...))
		if err == nil || stdout != "" || !strings.Contains(stderr, "changed nothing") || !strings.Contains(stderr, "restart `wacli sync`") {
			t.Fatalf("%v: err=%v stdout=%q stderr=%q", args, err, stdout, stderr)
		}
	}
}

func TestExecuteDelegatedAppStateWriteRejectsBadRequestsBeforeAppUse(t *testing.T) {
	fake := &fakeAppStateWriteApp{calls: make(chan string, 1)}
	for _, req := range []sendDelegateRequest{
		{Kind: appStateWriteKind},
		{Kind: appStateWriteKind, AppState: &appStateWriteRequest{Action: "label_rename"}},
		{Kind: appStateWriteKind, AppState: &appStateWriteRequest{Action: labelDeleteAction}},
		{Kind: appStateWriteKind, AppState: &appStateWriteRequest{Action: chatLabelAddAction, Ref: "7"}},
	} {
		if _, err := executeDelegatedAppStateWrite(context.Background(), fake, req); err == nil {
			t.Fatalf("accepted %+v", req.AppState)
		}
	}
	select {
	case call := <-fake.calls:
		t.Fatalf("bad request reached the app: %s", call)
	default:
	}
}

func TestDelegatedAppStateWriteDoesNotWaitForSendQueue(t *testing.T) {
	for _, kind := range []string{appStateWriteKind, appStateResyncKind} {
		t.Run(kind, func(t *testing.T) {
			serverConn, clientConn := net.Pipe()
			defer clientConn.Close()
			if err := clientConn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
				t.Fatal(err)
			}
			sendSlot := make(chan struct{}, 1) // empty: an earlier send owns the queue
			go handleSendDelegateConn(context.Background(), serverConn, func(_ context.Context, req sendDelegateRequest) (sendDelegateResponse, error) {
				if req.Kind == appStateResyncKind {
					return sendDelegateResponse{OK: true, AppStateResync: &app.AppStateResyncResult{Collection: req.Collection}}, nil
				}
				return sendDelegateResponse{OK: true, Action: req.AppState.Action, AppState: &appStateWriteResult{Action: req.AppState.Action}}, nil
			}, sendSlot, newSendPacer(sendSpacing{}))
			req := sendDelegateRequest{Version: sendDelegateVersion, Kind: kind, Collection: "regular", TimeoutMS: 1000, AppState: &appStateWriteRequest{Action: labelDeleteAction, Ref: "7"}}
			raw, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			// net.Pipe is unbuffered: a trailing encoder newline can deadlock
			// the writer when Decode finishes at its exact buffer boundary.
			if _, err := clientConn.Write(raw); err != nil {
				t.Fatal(err)
			}
			var resp sendDelegateResponse
			if err := json.NewDecoder(clientConn).Decode(&resp); err != nil {
				t.Fatal(err)
			}
			if !resp.OK || (kind == appStateWriteKind && (resp.AppState == nil || resp.AppState.Action != labelDeleteAction)) || (kind == appStateResyncKind && (resp.AppStateResync == nil || resp.AppStateResync.Collection != "regular")) {
				t.Fatalf("resp = %+v", resp)
			}
		})
	}
}

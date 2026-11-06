package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/lock"
	"github.com/openclaw/wacli/internal/store"
)

type fakeQuickWriteApp struct{ calls int }

func (f *fakeQuickWriteApp) CreateQuickReply(_ context.Context, in app.QuickReplyInput) (store.QuickReply, error) {
	f.calls++
	return store.QuickReply{ID: "1", Shortcut: *in.Shortcut, Message: *in.Message, Keywords: []string{}, LabelIDs: []string{}}, nil
}
func (f *fakeQuickWriteApp) EditQuickReply(_ context.Context, ref string, in app.QuickReplyInput) (store.QuickReply, error) {
	f.calls++
	return store.QuickReply{ID: ref, Shortcut: "hello", Keywords: []string{}, LabelIDs: []string{}}, nil
}
func (f *fakeQuickWriteApp) DeleteQuickReply(_ context.Context, ref string) (store.QuickReply, error) {
	f.calls++
	return store.QuickReply{ID: ref, Shortcut: "hello", Deleted: true, Keywords: []string{}, LabelIDs: []string{}}, nil
}
func (f *fakeQuickWriteApp) ResyncAppState(_ context.Context, name string) (app.AppStateResyncResult, error) {
	f.calls++
	return app.AppStateResyncResult{Collection: name, Completeness: "complete", QuickReplies: 2}, nil
}

func TestQuickReplyCLIUsesProductionDelegate(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	dir := shortPresenceDelegateStoreDir(t)
	lk, err := lock.Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lk.Release()
	f := &fakeQuickWriteApp{}
	requests := make(chan sendDelegateRequest, 1)
	stop, err := startSendDelegateServerForStore(t.Context(), dir, sendSpacing{}, func(ctx context.Context, req sendDelegateRequest) (sendDelegateResponse, error) {
		requests <- req
		if req.Kind == appStateResyncKind {
			return executeDelegatedAppStateResync(ctx, f, req)
		}
		return executeDelegatedAppStateWrite(ctx, f, req)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	for _, tc := range []struct {
		args         []string
		kind, action string
	}{
		{[]string{"quick-replies", "create", "--shortcut", "hello", "--message", "text", "--keyword", "one", "--keyword", "two"}, appStateWriteKind, quickReplyCreateAction},
		{[]string{"quick-replies", "edit", "1", "--clear-keywords"}, appStateWriteKind, quickReplyEditAction},
		{[]string{"quick-replies", "delete", "1"}, appStateWriteKind, quickReplyDeleteAction},
		{[]string{"app-state", "resync", "regular"}, appStateResyncKind, ""},
	} {
		stdout, stderr, err := runPresenceDelegateHelper(t, append([]string{"--store", dir, "--json", "--timeout", "5s"}, tc.args...))
		if err != nil {
			t.Fatalf("%v: %v %q %q", tc.args, err, stdout, stderr)
		}
		req := <-requests
		if req.Kind != tc.kind || req.Version != sendDelegateVersion {
			t.Fatalf("request=%+v", req)
		}
		if req.Kind == appStateWriteKind {
			if req.AppState == nil || req.AppState.Action != tc.action {
				t.Fatalf("payload=%+v", req.AppState)
			}
			if tc.action == quickReplyCreateAction && (*req.AppState.Shortcut != "hello" || *req.AppState.Message != "text" || len(*req.AppState.Keywords) != 2) {
				t.Fatalf("create payload=%+v", req.AppState)
			}
			if tc.action == quickReplyEditAction && (req.AppState.Keywords == nil || len(*req.AppState.Keywords) != 0) {
				t.Fatalf("clear keywords=%+v", req.AppState)
			}
		}
		var result struct {
			Success bool           `json:"success"`
			Data    map[string]any `json:"data"`
		}
		if err := json.NewDecoder(strings.NewReader(stdout)).Decode(&result); err != nil || !result.Success {
			t.Fatalf("result=%s error=%v", stdout, err)
		}
		if _, found := result.Data["lists"]; found {
			t.Fatal("resync unexpectedly depends on Lists")
		}
		if tc.kind == appStateResyncKind {
			if req.Collection != "regular" || result.Data["quick_replies"] != float64(2) || result.Data["completeness"] != "complete" {
				t.Fatalf("resync=%s request=%+v", stdout, req)
			}
		} else if result.Data["action"] != tc.action || result.Data["quick_reply"] == nil {
			t.Fatal(stdout)
		}
	}
}

func TestQuickReplyOldOwnerUpgradeHints(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	dir := shortPresenceDelegateStoreDir(t)
	lk, err := lock.Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lk.Release()
	stop, err := startSendDelegateServerForStore(t.Context(), dir, sendSpacing{}, func(_ context.Context, req sendDelegateRequest) (sendDelegateResponse, error) {
		return sendDelegateResponse{}, fmt.Errorf("unsupported send kind %q", req.Kind)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	for _, args := range [][]string{{"quick-replies", "delete", "1"}, {"app-state", "resync", "regular"}} {
		stdout, stderr, err := runPresenceDelegateHelper(t, append([]string{"--store", dir, "--timeout", "5s"}, args...))
		if err == nil || stdout != "" || !strings.Contains(stderr, "changed nothing") || !strings.Contains(stderr, "restart `wacli sync`") {
			t.Fatalf("old owner=%v %q %q", err, stdout, stderr)
		}
	}
}

func TestQuickReplyDelegatesBypassSendQueue(t *testing.T) {
	for _, kind := range []string{appStateWriteKind, appStateResyncKind} {
		t.Run(kind, func(t *testing.T) {
			server, client := net.Pipe()
			defer client.Close()
			if err := client.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
				t.Fatal(err)
			}
			go handleSendDelegateConn(t.Context(), server, func(_ context.Context, req sendDelegateRequest) (sendDelegateResponse, error) {
				return sendDelegateResponse{OK: true}, nil
			}, make(chan struct{}, 1), newSendPacer(sendSpacing{}))
			raw, err := json.Marshal(sendDelegateRequest{Version: sendDelegateVersion, Kind: kind, TimeoutMS: 1000, Collection: "regular", AppState: &appStateWriteRequest{Action: quickReplyDeleteAction, Ref: "1"}})
			if err != nil {
				t.Fatal(err)
			}
			// Omit the encoder newline: net.Pipe is unbuffered at decoder boundaries.
			if _, err := client.Write(raw); err != nil {
				t.Fatal(err)
			}
			var response sendDelegateResponse
			if err := json.NewDecoder(client).Decode(&response); err != nil || !response.OK {
				t.Fatalf("response=%+v %v", response, err)
			}
		})
	}
}

func TestQuickReplyCommandsValidateBeforeOpeningStore(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"quick-replies", "create", "--shortcut", "x"}, "--shortcut and --message are required"},
		{[]string{"quick-replies", "edit", "1"}, "nothing to change"},
		{[]string{"quick-replies", "edit", "1", "--keyword", "k", "--clear-keywords"}, "mutually exclusive"},
		{[]string{"--read-only", "quick-replies", "delete", "1"}, "read-only"},
		{[]string{"--read-only", "app-state", "resync", "regular"}, "read-only"},
		{[]string{"app-state", "resync", "regular_low"}, "only regular"},
		{[]string{"app-state", "resync", "regular", "--force"}, "unknown flag: --force"},
		{[]string{"lists", "list"}, "unknown command"},
	} {
		var err error
		captureRootStderr(t, func() { err = execute(append([]string{"--store", t.TempDir()}, tc.args...)) })
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%v: %v", tc.args, err)
		}
	}
	f := &fakeQuickWriteApp{}
	for _, req := range []sendDelegateRequest{{}, {AppState: &appStateWriteRequest{Action: "unknown"}}, {AppState: &appStateWriteRequest{Action: quickReplyDeleteAction}}, {AppState: &appStateWriteRequest{Action: quickReplyCreateAction}}} {
		if _, err := executeDelegatedAppStateWrite(t.Context(), f, req); err == nil {
			t.Fatal("invalid request reached app")
		}
	}
	if f.calls != 0 {
		t.Fatal("invalid request used app")
	}
	if err := writeAppStateWriteResult(&rootFlags{}, appStateWriteResult{Action: quickReplyCreateAction}); err == nil {
		t.Fatal("missing result accepted")
	}
}

func TestQuickReplyListReadOnlyJSONAndIncompleteHint(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "wacli.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertQuickReply(store.QuickReply{ID: "1", Shortcut: "hello", Message: "Synthetic"}, time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	db.Close()
	raw := captureRootStdout(t, func() {
		if err := execute([]string{"--store", dir, "--read-only", "--json", "quick-replies", "list"}); err != nil {
			t.Fatal(err)
		}
	})
	var result struct {
		Success bool                     `json:"success"`
		Data    store.QuickRepliesResult `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil || !result.Success || result.Data.Completeness != "incomplete" || len(result.Data.QuickReplies) != 1 || result.Data.QuickReplies[0].Keywords == nil {
		t.Fatalf("list=%s %v", raw, err)
	}
	stderr := captureRootStderr(t, func() {
		captureRootStdout(t, func() {
			if err := execute([]string{"--store", dir, "--read-only", "quick-replies", "list"}); err != nil {
				t.Fatal(err)
			}
		})
	})
	if !strings.Contains(stderr, "incomplete") || !strings.Contains(stderr, "app-state resync regular") {
		t.Fatalf("hint=%q", stderr)
	}
}

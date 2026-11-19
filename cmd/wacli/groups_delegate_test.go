package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/delegate"
	"github.com/openclaw/wacli/internal/lock"
	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow/types"
)

const (
	testGroupJID  = "120363000000000001@g.us"
	testParentJID = "120363000000000099@g.us"
	testInvite    = "https://chat.whatsapp.com/TESTINVITECODE"
)

type fakeGroupDelegateClient struct {
	mu      sync.Mutex
	creates []wa.CreateGroupRequest
	invites []string
}

func (f *fakeGroupDelegateClient) ResolveLIDToPN(_ context.Context, jid types.JID) types.JID {
	return jid
}

func (f *fakeGroupDelegateClient) CreateGroup(_ context.Context, req wa.CreateGroupRequest) (*types.GroupInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates = append(f.creates, req)
	return fakeCreatedGroupInfo(req), nil
}

func (f *fakeGroupDelegateClient) GetGroupInviteLink(_ context.Context, group types.JID, reset bool) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.invites = append(f.invites, fmt.Sprintf("%s reset=%t", group, reset))
	return testInvite, nil
}

func (f *fakeGroupDelegateClient) snapshot() ([]wa.CreateGroupRequest, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]wa.CreateGroupRequest(nil), f.creates...), append([]string(nil), f.invites...)
}

func fakeCreatedGroupInfo(req wa.CreateGroupRequest) *types.GroupInfo {
	jid, _ := types.ParseJID(testGroupJID)
	info := &types.GroupInfo{
		JID:              jid,
		OwnerJID:         types.NewJID("15550000000", types.DefaultUserServer),
		GroupName:        types.GroupName{Name: req.Name},
		GroupLocked:      types.GroupLocked{IsLocked: req.IsLocked},
		GroupAnnounce:    types.GroupAnnounce{IsAnnounce: req.IsAnnounce},
		GroupParent:      types.GroupParent{IsParent: req.IsParent},
		GroupCreated:     time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC),
		ParticipantCount: len(req.Participants),
		GroupMembershipApprovalMode: types.GroupMembershipApprovalMode{
			IsJoinApprovalRequired: req.IsJoinApprovalRequired,
		},
	}
	if !req.LinkedParentJID.IsEmpty() {
		info.LinkedParentJID = req.LinkedParentJID
	}
	for _, p := range req.Participants {
		info.Participants = append(info.Participants, types.GroupParticipant{JID: p})
	}
	return info
}

// commandStdout drops the test framework's trailing PASS line that the
// helper process prints after the command's own output.
func commandStdout(stdout string) string {
	return strings.TrimSuffix(stdout, "PASS\n")
}

func openGroupDelegateTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "wacli.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// startLockedGroupDelegate holds the store lock like `sync --follow` and
// serves group requests through the production delegate server.
func startLockedGroupDelegate(t *testing.T, execute sendDelegateExecutor) string {
	t.Helper()
	storeDir := shortPresenceDelegateStoreDir(t)
	lk, err := lock.Acquire(storeDir)
	if err != nil {
		t.Fatalf("lock store: %v", err)
	}
	t.Cleanup(func() { _ = lk.Release() })
	stop, err := startSendDelegateServerForStore(context.Background(), storeDir, sendSpacing{}, execute)
	if err != nil {
		t.Fatalf("start delegate server: %v", err)
	}
	t.Cleanup(stop)
	return storeDir
}

func groupDelegateExecutor(client *fakeGroupDelegateClient, db *store.DB) sendDelegateExecutor {
	return func(ctx context.Context, req sendDelegateRequest) (sendDelegateResponse, error) {
		if req.Version != sendDelegateVersion {
			return sendDelegateResponse{}, fmt.Errorf("unexpected version %d", req.Version)
		}
		switch req.Kind {
		case groupCreateKind:
			return executeDelegatedGroupCreate(ctx, client, db, req)
		case groupInviteLinkKind:
			return executeDelegatedGroupInviteLink(ctx, client, req)
		default:
			return sendDelegateResponse{}, fmt.Errorf("unsupported send kind %q", req.Kind)
		}
	}
}

func TestGroupsCreateDelegatesThroughProductionServerWhenStoreLocked(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	client := &fakeGroupDelegateClient{}
	db := openGroupDelegateTestDB(t)
	storeDir := startLockedGroupDelegate(t, groupDelegateExecutor(client, db))

	stdout, stderr, err := runPresenceDelegateHelper(t, []string{
		"--store", storeDir, "--json", "--timeout", "2s",
		"groups", "create", "--name", "Team",
		"--user", "+1 555 000 0001", "--user", "15550000002@s.whatsapp.net",
		"--announce-only", "--locked", "--join-approval", "--linked-parent", testParentJID,
	})
	if err != nil {
		t.Fatalf("groups create: %v stdout=%q stderr=%q", err, stdout, stderr)
	}

	creates, _ := client.snapshot()
	if len(creates) != 1 {
		t.Fatalf("CreateGroup calls = %d, want 1", len(creates))
	}
	got := creates[0]
	wantUsers := []string{"15550000001@s.whatsapp.net", "15550000002@s.whatsapp.net"}
	if got.Name != "Team" || len(got.Participants) != len(wantUsers) || !got.IsAnnounce || !got.IsLocked || !got.IsJoinApprovalRequired || got.IsParent || got.LinkedParentJID.String() != testParentJID {
		t.Fatalf("delegated create request = %+v", got)
	}
	for i, want := range wantUsers {
		if got.Participants[i].String() != want {
			t.Fatalf("participant %d = %s, want %s", i, got.Participants[i], want)
		}
	}

	// The delegated result must print exactly what the direct command prints.
	var want bytes.Buffer
	if err := out.WriteJSON(&want, fakeCreatedGroupInfo(got)); err != nil {
		t.Fatalf("encode direct output: %v", err)
	}
	if commandStdout(stdout) != want.String() {
		t.Fatalf("stdout = %q\nwant direct output %q", stdout, want.String())
	}

	// The follow process persists the new group in its own store.
	groups, err := db.ListGroups("Team", 1)
	if err != nil {
		t.Fatalf("ListGroups: %v", err)
	}
	if len(groups) != 1 || groups[0].JID != testGroupJID || groups[0].LinkedParentJID != testParentJID {
		t.Fatalf("persisted groups = %+v", groups)
	}
}

func TestGroupsCreateDelegatedTextOutputMatchesDirect(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	client := &fakeGroupDelegateClient{}
	storeDir := startLockedGroupDelegate(t, groupDelegateExecutor(client, openGroupDelegateTestDB(t)))

	stdout, stderr, err := runPresenceDelegateHelper(t, []string{
		"--store", storeDir, "--timeout", "2s", "groups", "create", "--name", "Team", "--community",
	})
	if err != nil {
		t.Fatalf("groups create: %v stdout=%q stderr=%q", err, stdout, stderr)
	}
	if want := "JID: " + testGroupJID + "\nName: Team\n"; commandStdout(stdout) != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
	creates, _ := client.snapshot()
	if len(creates) != 1 || !creates[0].IsParent {
		t.Fatalf("create calls = %+v, want one community parent", creates)
	}
}

func TestGroupsInviteLinkDelegatesThroughProductionServerWhenStoreLocked(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	tests := []struct {
		action   string
		wantCall string
		wantJSON map[string]any
	}{
		{
			action:   "get",
			wantCall: testGroupJID + " reset=false",
			wantJSON: map[string]any{"jid": testGroupJID, "link": testInvite},
		},
		{
			action:   "revoke",
			wantCall: testGroupJID + " reset=true",
			wantJSON: map[string]any{"jid": testGroupJID, "link": testInvite, "revoked": true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.action, func(t *testing.T) {
			client := &fakeGroupDelegateClient{}
			storeDir := startLockedGroupDelegate(t, groupDelegateExecutor(client, nil))

			stdout, stderr, err := runPresenceDelegateHelper(t, []string{
				"--store", storeDir, "--json", "--timeout", "2s", "groups", "invite", "link", tt.action, "--jid", testGroupJID,
			})
			if err != nil {
				t.Fatalf("invite link %s: %v stdout=%q stderr=%q", tt.action, err, stdout, stderr)
			}
			_, invites := client.snapshot()
			if len(invites) != 1 || invites[0] != tt.wantCall {
				t.Fatalf("invite calls = %q, want %q", invites, tt.wantCall)
			}
			var want bytes.Buffer
			if err := out.WriteJSON(&want, tt.wantJSON); err != nil {
				t.Fatalf("encode direct output: %v", err)
			}
			if commandStdout(stdout) != want.String() {
				t.Fatalf("stdout = %q, want direct output %q", stdout, want.String())
			}

			stdout, stderr, err = runPresenceDelegateHelper(t, []string{
				"--store", storeDir, "--timeout", "2s", "groups", "invite", "link", tt.action, "--jid", testGroupJID,
			})
			if err != nil || commandStdout(stdout) != testInvite+"\n" {
				t.Fatalf("text output: %v stdout=%q stderr=%q", err, stdout, stderr)
			}
		})
	}
}

func TestGroupsDelegationRespectsReadOnly(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	commands := map[string][]string{
		"create": {"groups", "create", "--name", "Team"},
		"revoke": {"groups", "invite", "link", "revoke", "--jid", testGroupJID},
		"get":    {"groups", "invite", "link", "get", "--jid", testGroupJID},
	}
	for name, command := range commands {
		for _, mode := range []string{"flag", "environment"} {
			t.Run(name+"/"+mode, func(t *testing.T) {
				called := make(chan string, 1)
				storeDir := startLockedGroupDelegate(t, func(_ context.Context, req sendDelegateRequest) (sendDelegateResponse, error) {
					called <- req.Kind
					return sendDelegateResponse{OK: true}, nil
				})
				args := []string{"--store", storeDir, "--json", "--timeout", "2s"}
				t.Setenv("WACLI_READONLY", "")
				if mode == "flag" {
					args = append(args, "--read-only")
				} else {
					t.Setenv("WACLI_READONLY", "1")
				}
				stdout, stderr, err := runPresenceDelegateHelper(t, append(args, command...))
				if err == nil || !strings.Contains(stdout+stderr, "read-only") {
					t.Fatalf("want read-only rejection, got %v stdout=%q stderr=%q", err, stdout, stderr)
				}
				select {
				case kind := <-called:
					t.Fatalf("read-only %s reached the delegate as %q", name, kind)
				default:
				}
			})
		}
	}
}

func TestGroupsDelegationExplainsOlderDaemon(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	// A daemon from before #551 rejects both kinds in executeDelegatedSend.
	storeDir := startLockedGroupDelegate(t, func(_ context.Context, req sendDelegateRequest) (sendDelegateResponse, error) {
		return sendDelegateResponse{}, fmt.Errorf("unsupported send kind %q", req.Kind)
	})
	commands := map[string][]string{
		"groups create":      {"groups", "create", "--name", "Team"},
		"groups invite link": {"groups", "invite", "link", "get", "--jid", testGroupJID},
	}
	for want, command := range commands {
		t.Run(want, func(t *testing.T) {
			stdout, stderr, err := runPresenceDelegateHelper(t, append([]string{"--store", storeDir, "--timeout", "2s"}, command...))
			if err == nil {
				t.Fatalf("older daemon accepted %s: stdout=%q", want, stdout)
			}
			if !strings.Contains(stderr, "does not support delegated "+want) || !strings.Contains(stderr, "restart `wacli sync`") {
				t.Fatalf("stderr = %q, want the restart hint for %s", stderr, want)
			}
		})
	}
}

func TestExplainGroupDelegateErrorLeavesOtherErrorsUnchanged(t *testing.T) {
	other := errors.New("connection refused")
	if got := explainGroupDelegateError(other, groupCreateKind, "groups create"); got != other {
		t.Fatalf("unrelated error = %v, want it unchanged", got)
	}
	// Another kind's rejection is not this command's upgrade problem.
	contacts := fmt.Errorf("unsupported send kind %q", contactsSaveKind)
	if got := explainGroupDelegateError(contacts, groupCreateKind, "groups create"); got != contacts {
		t.Fatalf("other kind = %v, want it unchanged", got)
	}
}

func TestExecuteDelegatedGroupCreateValidatesBeforeCreating(t *testing.T) {
	tests := []struct {
		name string
		req  sendDelegateRequest
		want string
	}{
		{name: "missing name", req: sendDelegateRequest{Name: "  "}, want: "--name is required"},
		{name: "community and parent", req: sendDelegateRequest{Name: "Team", GroupCommunity: true, GroupLinkedParent: testParentJID}, want: "cannot be combined"},
		{name: "bad user", req: sendDelegateRequest{Name: "Team", Users: []string{"not a phone"}}, want: ""},
		{name: "parent is not a group", req: sendDelegateRequest{Name: "Team", GroupLinkedParent: "15550000001@s.whatsapp.net"}, want: "parse --linked-parent"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &fakeGroupDelegateClient{}
			tt.req.Kind = groupCreateKind
			_, err := executeDelegatedGroupCreate(context.Background(), client, nil, tt.req)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
			if creates, _ := client.snapshot(); len(creates) != 0 {
				t.Fatalf("invalid request reached CreateGroup: %+v", creates)
			}
		})
	}
}

func TestExecuteDelegatedGroupInviteLinkRejectsMissingJID(t *testing.T) {
	client := &fakeGroupDelegateClient{}
	_, err := executeDelegatedGroupInviteLink(context.Background(), client, sendDelegateRequest{Kind: groupInviteLinkKind, GroupResetInvite: true})
	if err == nil || !strings.Contains(err.Error(), "--jid is required") {
		t.Fatalf("error = %v, want missing jid", err)
	}
	if _, invites := client.snapshot(); len(invites) != 0 {
		t.Fatalf("request without a group reached WhatsApp: %q", invites)
	}
}

func TestGroupDelegatesQueuedBehindSendHonorCallerDeadline(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	requests := map[string]sendDelegateRequest{
		groupCreateKind:     {Kind: groupCreateKind, Name: "Team"},
		groupInviteLinkKind: {Kind: groupInviteLinkKind, To: testGroupJID, GroupResetInvite: true},
	}
	for kind, req := range requests {
		t.Run(kind, func(t *testing.T) {
			storeDir := shortPresenceDelegateStoreDir(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			started := make(chan struct{})
			release := make(chan struct{})
			dispatched := make(chan struct{}, 1)
			stop, err := startSendDelegateServerForStore(ctx, storeDir, sendSpacing{}, func(ctx context.Context, got sendDelegateRequest) (sendDelegateResponse, error) {
				switch got.Kind {
				case "text":
					close(started)
					select {
					case <-release:
						return sendDelegateResponse{OK: true}, nil
					case <-ctx.Done():
						return sendDelegateResponse{}, ctx.Err()
					}
				case kind:
					dispatched <- struct{}{}
					return sendDelegateResponse{OK: true}, nil
				default:
					return sendDelegateResponse{}, fmt.Errorf("unexpected kind %q", got.Kind)
				}
			})
			if err != nil {
				t.Fatalf("start delegate server: %v", err)
			}
			defer stop()
			defer close(release)

			// A send holds the serialized slot while the group request's
			// absolute caller deadline expires.
			sendDone := make(chan error, 1)
			go func() {
				_, err := delegateSend(ctx, &rootFlags{storeDir: storeDir, timeout: 3 * time.Second}, sendDelegateRequest{Kind: "text"})
				sendDone <- err
			}()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("send did not acquire the slot")
			}

			conn, err := delegate.Dial(ctx, storeDir)
			if err != nil {
				t.Fatalf("dial delegate socket: %v", err)
			}
			defer conn.Close()
			if err := conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatalf("set client deadline: %v", err)
			}
			req.Version = sendDelegateVersion
			req.TimeoutMS = durationMillis(5 * time.Second)
			req.DeadlineUnixMS = time.Now().Add(150 * time.Millisecond).UnixMilli()
			if err := json.NewEncoder(conn).Encode(req); err != nil {
				t.Fatalf("encode request: %v", err)
			}
			var resp sendDelegateResponse
			if err := json.NewDecoder(conn).Decode(&resp); err != nil {
				t.Fatalf("queued %s did not receive an explicit refusal: %v", kind, err)
			}
			if resp.OK || !strings.Contains(resp.Error, "it was not sent") {
				t.Fatalf("response = %+v, want refusal before dispatch", resp)
			}

			release <- struct{}{}
			if err := <-sendDone; err != nil {
				t.Fatalf("blocking send: %v", err)
			}
			select {
			case <-dispatched:
				t.Fatalf("expired %s was dispatched", kind)
			case <-time.After(50 * time.Millisecond):
			}
		})
	}
}

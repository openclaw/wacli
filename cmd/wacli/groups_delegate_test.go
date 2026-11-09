package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/lock"
	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow/types"
)

// fakeGroupClient stands in for the sync daemon's connected WhatsApp client.
type fakeGroupClient struct {
	mu     sync.Mutex
	calls  []string
	info   map[string]*types.GroupInfo
	hidden map[string]bool
	err    error
}

func newFakeGroupClient() *fakeGroupClient {
	return &fakeGroupClient{info: map[string]*types.GroupInfo{}, hidden: map[string]bool{}}
}

func (f *fakeGroupClient) record(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf(format, args...))
}

func (f *fakeGroupClient) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeGroupClient) ResolveLIDToPN(_ context.Context, jid types.JID) types.JID { return jid }

func (f *fakeGroupClient) GetJoinedGroups(context.Context) ([]*types.GroupInfo, error) {
	f.record("joined")
	var out []*types.GroupInfo
	for _, g := range f.info {
		out = append(out, g)
	}
	return out, f.err
}

func (f *fakeGroupClient) GetGroupInfo(_ context.Context, jid types.JID) (*types.GroupInfo, error) {
	f.record("info %s", jid)
	if f.err != nil {
		return nil, f.err
	}
	return f.info[jid.String()], nil
}

func (f *fakeGroupClient) CreateGroup(_ context.Context, req wa.CreateGroupRequest) (*types.GroupInfo, error) {
	f.record("create %s locked=%t users=%d", req.Name, req.IsLocked, len(req.Participants))
	if f.err != nil {
		return nil, f.err
	}
	info := &types.GroupInfo{JID: types.NewJID("120363000000000001", types.GroupServer)}
	info.GroupName.Name = req.Name
	f.info[info.JID.String()] = info
	return info, nil
}

func (f *fakeGroupClient) SetGroupName(_ context.Context, jid types.JID, name string) error {
	f.record("rename %s %s", jid, name)
	return f.err
}

func (f *fakeGroupClient) SetGroupTopic(_ context.Context, jid types.JID, topic string) error {
	f.record("topic %s %s", jid, topic)
	return f.err
}

func (f *fakeGroupClient) UpdateGroupParticipants(_ context.Context, group types.JID, users []types.JID, action wa.GroupParticipantAction) ([]types.GroupParticipant, error) {
	f.record("participants %s %s %d", action, group, len(users))
	if f.err != nil {
		return nil, f.err
	}
	out := make([]types.GroupParticipant, 0, len(users))
	for _, u := range users {
		out = append(out, types.GroupParticipant{JID: u})
	}
	return out, nil
}

func (f *fakeGroupClient) GetGroupInviteLink(_ context.Context, group types.JID, reset bool) (string, error) {
	f.record("invite %s reset=%t", group, reset)
	return "https://chat.whatsapp.com/abc", f.err
}

func (f *fakeGroupClient) SetGroupPhoto(_ context.Context, jid types.JID, avatar []byte) (string, error) {
	f.record("photo %s bytes=%d nil=%t", jid, len(avatar), avatar == nil)
	return "pic-1", f.err
}

func (f *fakeGroupClient) LinkGroup(_ context.Context, parent, child types.JID, hidden bool) error {
	f.record("link %s -> %s hidden=%t", child, parent, hidden)
	if f.err != nil {
		return f.err
	}
	if info := f.info[child.String()]; info != nil {
		info.LinkedParentJID = parent
	}
	f.hidden[child.String()] = hidden
	return nil
}

func (f *fakeGroupClient) GroupHidden(_ context.Context, jid types.JID) (bool, error) {
	f.record("hidden? %s", jid)
	return f.hidden[jid.String()], nil
}

func (f *fakeGroupClient) UnlinkGroup(_ context.Context, parent, child types.JID) error {
	f.record("unlink %s -> %s", child, parent)
	return f.err
}

type fakeGroupStore struct {
	mu        sync.Mutex
	persisted []string
	joined    int
}

func (s *fakeGroupStore) PersistGroup(_ context.Context, info *types.GroupInfo) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.persisted = append(s.persisted, info.JID.String())
	return nil
}

func (s *fakeGroupStore) ReplaceJoinedGroups(_ context.Context, groups []*types.GroupInfo, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.joined = len(groups)
	return nil
}

const (
	testGroupJID     = "120363432382555309@g.us"
	testCommunityJID = "120363400000000000@g.us"
)

func fakeWithGroup() *fakeGroupClient {
	f := newFakeGroupClient()
	g := &types.GroupInfo{JID: types.NewJID("120363432382555309", types.GroupServer)}
	g.GroupName.Name = "AOM x Cofoundy"
	f.info[testGroupJID] = g
	return f
}

func TestRunGroupOpLinkHiddenThenInfoReportsHidden(t *testing.T) {
	client := fakeWithGroup()
	st := &fakeGroupStore{}
	ctx := context.Background()

	res, err := runGroupOp(ctx, client, st, groupOp{Action: groupActionLink, JID: testGroupJID, Parent: testCommunityJID, Hidden: true})
	if err != nil {
		t.Fatalf("link: %v", err)
	}
	var linked map[string]any
	if err := json.Unmarshal(res.JSON, &linked); err != nil {
		t.Fatalf("link json: %v", err)
	}
	if linked["hidden"] != true || linked["linked"] != true || linked["parent"] != testCommunityJID {
		t.Fatalf("link json = %v", linked)
	}

	res, err = runGroupOp(ctx, client, st, groupOp{Action: groupActionInfo, JID: testGroupJID})
	if err != nil {
		t.Fatalf("info: %v", err)
	}
	for _, want := range []string{"Type: subgroup", "Parent: " + testCommunityJID, "Hidden: true"} {
		if !strings.Contains(res.Text, want) {
			t.Fatalf("info text %q missing %q", res.Text, want)
		}
	}
	var info map[string]any
	if err := json.Unmarshal(res.JSON, &info); err != nil {
		t.Fatalf("info json: %v", err)
	}
	if info["Hidden"] != true || info["LinkedParentJID"] != testCommunityJID || info["JID"] != testGroupJID {
		t.Fatalf("info json = %s", res.JSON)
	}
	if len(st.persisted) < 2 {
		t.Fatalf("snapshots persisted = %v, want link refresh and info", st.persisted)
	}
}

func TestRunGroupOpInfoOmitsHiddenWhenNotLinked(t *testing.T) {
	res, err := runGroupOp(context.Background(), fakeWithGroup(), &fakeGroupStore{}, groupOp{Action: groupActionInfo, JID: testGroupJID})
	if err != nil {
		t.Fatalf("info: %v", err)
	}
	if strings.Contains(res.Text, "Parent:") || strings.Contains(res.Text, "Hidden:") {
		t.Fatalf("unlinked group text = %q", res.Text)
	}
	if strings.Contains(string(res.JSON), `"Hidden"`) {
		t.Fatalf("unlinked group json reports Hidden: %s", res.JSON)
	}
}

func TestRunGroupOpPictureRemoveSendsNil(t *testing.T) {
	client := fakeWithGroup()
	res, err := runGroupOp(context.Background(), client, &fakeGroupStore{}, groupOp{Action: groupActionPicture, JID: testGroupJID})
	if err != nil {
		t.Fatalf("remove picture: %v", err)
	}
	if res.Text != "Group picture removed\n" {
		t.Fatalf("text = %q", res.Text)
	}
	if got := client.Calls(); len(got) != 1 || !strings.HasSuffix(got[0], "bytes=0 nil=true") {
		t.Fatalf("calls = %v", got)
	}
}

func TestRunGroupOpRejectsBadInputBeforeClientUse(t *testing.T) {
	tests := []groupOp{
		{Action: groupActionParticipants, JID: testGroupJID, Users: []string{"+51999"}, ParticipantAction: "kick"},
		{Action: groupActionLink, JID: testGroupJID},
		{Action: groupActionLink, JID: "123@s.whatsapp.net", Parent: testCommunityJID},
		{Action: groupActionCreate},
		{Action: "explode"},
	}
	for _, op := range tests {
		client := newFakeGroupClient()
		if _, err := runGroupOp(context.Background(), client, &fakeGroupStore{}, op); err == nil {
			t.Fatalf("op %+v succeeded, want validation error", op)
		}
		if calls := client.Calls(); len(calls) != 0 {
			t.Fatalf("op %+v reached the client: %v", op, calls)
		}
	}
}

func TestExecuteDelegatedGroupRequiresOperation(t *testing.T) {
	_, err := executeDelegatedGroup(context.Background(), newFakeGroupClient(), &fakeGroupStore{}, sendDelegateRequest{Kind: groupOpKind})
	if err == nil || !strings.Contains(err.Error(), "without an operation") {
		t.Fatalf("error = %v", err)
	}
}

func TestGroupDelegateRequestRoundTripsImageBytes(t *testing.T) {
	req := sendDelegateRequest{Version: sendDelegateVersion, Kind: groupOpKind, Group: &groupOp{Action: groupActionPicture, JID: testGroupJID, Image: []byte{0xff, 0xd8, 0x00, 0xff}}}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var back sendDelegateRequest
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.Group == nil || string(back.Group.Image) != string(req.Group.Image) {
		t.Fatalf("image did not survive the socket encoding: %+v", back.Group)
	}
}

// A group operation must not wait behind a queued send or the send pacer.
func TestDelegatedGroupDoesNotWaitForSendQueue(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	if err := clientConn.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set client deadline: %v", err)
	}
	client := fakeWithGroup()
	sendSlot := make(chan struct{}, 1) // empty: an earlier send owns it
	go handleSendDelegateConn(context.Background(), serverConn, func(ctx context.Context, req sendDelegateRequest) (sendDelegateResponse, error) {
		return executeDelegatedGroup(ctx, client, &fakeGroupStore{}, req)
	}, sendSlot, newSendPacer(sendSpacing{min: time.Second, max: time.Second}))

	req := sendDelegateRequest{Version: sendDelegateVersion, Kind: groupOpKind, Group: &groupOp{Action: groupActionInfo, JID: testGroupJID}}
	if err := json.NewEncoder(clientConn).Encode(req); err != nil {
		t.Fatalf("encode: %v", err)
	}
	var resp sendDelegateResponse
	if err := json.NewDecoder(clientConn).Decode(&resp); err != nil {
		t.Fatalf("group op waited behind the send queue: %v", err)
	}
	if !resp.OK || resp.Group == nil || !strings.Contains(resp.Group.Text, "AOM x Cofoundy") {
		t.Fatalf("response = %+v", resp)
	}
}

// startFakeGroupDaemon locks a store like `sync --follow` does and serves
// group operations on its control socket with a fake client.
func startFakeGroupDaemon(t *testing.T, client *fakeGroupClient, execute sendDelegateExecutor) string {
	t.Helper()
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	storeDir := shortPresenceDelegateStoreDir(t)
	lk, err := lock.Acquire(storeDir)
	if err != nil {
		t.Fatalf("lock store: %v", err)
	}
	t.Cleanup(func() { _ = lk.Release() })
	if execute == nil {
		execute = func(ctx context.Context, req sendDelegateRequest) (sendDelegateResponse, error) {
			if req.Version != sendDelegateVersion || req.Kind != groupOpKind {
				return sendDelegateResponse{}, fmt.Errorf("unexpected delegated request %d/%q", req.Version, req.Kind)
			}
			return executeDelegatedGroup(ctx, client, &fakeGroupStore{}, req)
		}
	}
	stop, err := startSendDelegateServerForStore(context.Background(), storeDir, sendSpacing{}, execute)
	if err != nil {
		t.Fatalf("start delegate server: %v", err)
	}
	t.Cleanup(stop)
	return storeDir
}

// The 9-nov failure: with the sync holding the lock, `groups link --hidden`
// and `groups info` must go through the sync instead of timing out on the lock.
func TestGroupsCommandsDelegateToSyncWhenStoreLocked(t *testing.T) {
	client := fakeWithGroup()
	storeDir := startFakeGroupDaemon(t, client, nil)

	stdout, stderr, err := runPresenceDelegateHelper(t, []string{"--store", storeDir, "--json", "--timeout", "2s",
		"groups", "link", "--jid", testGroupJID, "--parent", testCommunityJID, "--hidden"})
	if err != nil {
		t.Fatalf("groups link failed: %v stdout=%q stderr=%q", err, stdout, stderr)
	}
	if !strings.Contains(stdout, `"success":true`) || !strings.Contains(stdout, `"hidden":true`) {
		t.Fatalf("link stdout = %q", stdout)
	}

	stdout, stderr, err = runPresenceDelegateHelper(t, []string{"--store", storeDir, "--timeout", "2s",
		"groups", "info", "--jid", testGroupJID})
	if err != nil {
		t.Fatalf("groups info failed: %v stdout=%q stderr=%q", err, stdout, stderr)
	}
	for _, want := range []string{"Name: AOM x Cofoundy", "Parent: " + testCommunityJID, "Hidden: true"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("info stdout %q missing %q", stdout, want)
		}
	}

	stdout, stderr, err = runPresenceDelegateHelper(t, []string{"--store", storeDir, "--json", "--timeout", "2s",
		"groups", "participants", "add", "--jid", testGroupJID, "--user", "+51999888777", "--user", "+51999888666"})
	if err != nil {
		t.Fatalf("participants add failed: %v stdout=%q stderr=%q", err, stdout, stderr)
	}
	if !strings.Contains(stdout, "51999888777@s.whatsapp.net") {
		t.Fatalf("participants stdout = %q", stdout)
	}

	calls := strings.Join(client.Calls(), "\n")
	for _, want := range []string{
		"link " + testGroupJID + " -> " + testCommunityJID + " hidden=true",
		"hidden? " + testGroupJID,
		"participants add " + testGroupJID + " 2",
	} {
		if !strings.Contains(calls, want) {
			t.Fatalf("daemon calls missing %q:\n%s", want, calls)
		}
	}
}

// An older sync (upstream wacli) answers "unsupported send kind": the command
// must say so and must not fall back to waiting on the lock it holds.
func TestGroupsCommandExplainsOldSyncWithoutGroupSupport(t *testing.T) {
	storeDir := startFakeGroupDaemon(t, nil, func(context.Context, sendDelegateRequest) (sendDelegateResponse, error) {
		return sendDelegateResponse{}, fmt.Errorf("unsupported send kind %q", groupOpKind)
	})
	stdout, stderr, err := runPresenceDelegateHelper(t, []string{"--store", storeDir, "--timeout", "2s",
		"groups", "info", "--jid", testGroupJID})
	if err == nil {
		t.Fatalf("info succeeded against an old sync: stdout=%q", stdout)
	}
	if !strings.Contains(stderr, "does not support group operations") || strings.Contains(stderr, "timed out waiting for store lock") {
		t.Fatalf("stderr = %q, want the old-sync explanation", stderr)
	}
}

// With no sync listening, a held lock is reported as a lock, naming why the
// command needed it, never as some other state.
func TestGroupsCommandNamesLockWhenNoSyncAnswers(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	storeDir := shortPresenceDelegateStoreDir(t)
	lk, err := lock.Acquire(storeDir)
	if err != nil {
		t.Fatalf("lock store: %v", err)
	}
	defer lk.Release()

	stdout, stderr, err := runPresenceDelegateHelper(t, []string{"--store", storeDir, "--timeout", "2s",
		"groups", "info", "--jid", testGroupJID})
	if err == nil {
		t.Fatalf("info succeeded with the store locked and no sync: stdout=%q", stdout)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "lock") || !strings.Contains(stderr, "control socket") {
		t.Fatalf("stderr = %q, want the lock named", stderr)
	}
}

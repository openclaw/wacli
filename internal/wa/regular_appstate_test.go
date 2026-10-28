package wa

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	appStore "github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/proto/waServerSync"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	waStore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// Only IQ transport/delivery scheduling is replaced. Both batches are encoded,
// MAC-verified, applied to session.db and converted to events by the pinned SDK.
type delayedRegularSDK struct {
	sdkRegularAppStateSender
	patch      *waServerSync.SyncdPatch
	start      appstate.HashState
	late       func()
	n          int
	persist    func([]any)
	responses  []*waBinary.Node
	requests   int
	fetchError error
}

func (s *delayedRegularSDK) exchange(ctx context.Context, _ appstate.WAPatchName, version uint64, encoded []byte) (*waBinary.Node, error) {
	s.requests++
	s.patch = &waServerSync.SyncdPatch{}
	if err := proto.Unmarshal(encoded, s.patch); err != nil {
		return nil, err
	}
	s.patch.Version = &waServerSync.SyncdVersion{Version: proto.Uint64(version + 1)}
	var err error
	s.start, err = s.state(ctx, appstate.WAPatchRegular)
	if len(s.responses) > 0 {
		response := s.responses[0]
		s.responses = s.responses[1:]
		return response, err
	}
	return &waBinary.Node{Tag: "iq", Content: []waBinary.Node{{Tag: "sync", Content: []waBinary.Node{{Tag: "collection"}}}}}, err
}

func (s *delayedRegularSDK) fetch(ctx context.Context, name appstate.WAPatchName) ([]any, error) {
	if s.fetchError != nil {
		return nil, s.fetchError
	}
	var batch []any
	_, err := s.cli.DangerousInternals().ApplyAppStatePatches(ctx, name, s.start, &appstate.PatchList{Name: name, Patches: []*waServerSync.SyncdPatch{s.patch}}, false, &batch)
	return batch, err
}

// Counterfactual models exactly SendAppState's detached batch dispatch: A is
// already applied in session.db, but its event goroutine resumes after B's.
func (s *delayedRegularSDK) send(ctx context.Context, p appstate.PatchInfo) error {
	state, err := s.state(ctx, p.Type)
	if err != nil {
		return err
	}
	raw, err := s.encode(ctx, state, p)
	if err != nil {
		return err
	}
	if _, err := s.exchange(ctx, p.Type, state.Version, raw); err != nil {
		return err
	}
	batch, err := s.fetch(ctx, p.Type)
	if err != nil {
		return err
	}
	s.n++
	if s.n == 1 {
		s.late = func() { s.persist(batch) }
	} else {
		s.persist(batch)
	}
	return nil
}

func TestRegularSDKSendBatchesCannotArriveAfterNewerWrites(t *testing.T) {
	for _, kind := range []string{"reply-edit", "reply-delete", "label-edit", "label-delete", "membership-remove"} {
		for _, boundary := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/captured=%t", kind, boundary), func(t *testing.T) {
				c, err := New(Options{StorePath: newPairedSessionStore(t)})
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				id := []byte{1, 2, 3, 4}
				if err := c.client.Store.AppStateKeys.PutAppStateSyncKey(t.Context(), id, waStore.AppStateSyncKey{Data: bytes.Repeat([]byte{7}, 32), Fingerprint: []byte{1}, Timestamp: 1}); err != nil {
					t.Fatal(err)
				}
				db, err := appStore.Open(filepath.Join(t.TempDir(), "wacli.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				chat := types.NewJID("123", types.DefaultUserServer)
				if kind == "membership-remove" {
					if err := db.UpsertList(appStore.ListUpdate{ID: "7", Name: proto.String("Fixture")}); err != nil {
						t.Fatal(err)
					}
					if err := db.UpsertChat(chat.String(), "dm", "Synthetic", time.Unix(1, 0)); err != nil {
						t.Fatal(err)
					}
				}
				persist := func(batch []any) {
					for _, evt := range batch {
						var err error
						switch v := evt.(type) {
						case *events.AppState:
							if act := v.GetQuickReplyAction(); act != nil {
								err = db.UpsertQuickReply(appStore.QuickReply{ID: "1", Shortcut: "hello", Message: act.GetMessage(), Deleted: act.GetDeleted()}, time.UnixMilli(v.GetTimestamp()))
							}
						case *events.LabelEdit:
							err = db.UpsertList(appStore.ListUpdate{ID: v.LabelID, Name: v.Action.Name, Deleted: v.Action.Deleted, Timestamp: v.Timestamp})
						case *events.LabelAssociationChat:
							err = db.SetChatList(v.JID.String(), v.LabelID, v.Action.GetLabeled(), v.Timestamp, false)
						}
						if err != nil {
							t.Fatal(err)
						}
					}
				}
				c.client.AddEventHandler(func(evt any) { persist([]any{evt}) })
				s := &delayedRegularSDK{sdkRegularAppStateSender: sdkRegularAppStateSender{cli: c.client}, persist: func(batch []any) {
					for _, evt := range batch {
						c.client.DangerousInternals().DispatchEvent(evt)
					}
				}}
				for step := range 2 {
					p := appstate.PatchInfo{Type: appstate.WAPatchRegular, Timestamp: time.Unix(int64(100-step), 0)}
					switch kind {
					case "reply-edit", "reply-delete":
						p.Mutations = []appstate.MutationInfo{{Index: []string{appstate.IndexQuickReply, "1"}, Version: 3, Value: &waSyncAction.SyncActionValue{QuickReplyAction: &waSyncAction.QuickReplyAction{Shortcut: proto.String("hello"), Message: proto.String([]string{"A", "B"}[step]), Deleted: proto.Bool(kind == "reply-delete" && step == 1)}}}}
					case "label-edit", "label-delete":
						p = appstate.BuildLabelEdit("7", []string{"A", "B"}[step], 1, kind == "label-delete" && step == 1)
						p.Timestamp = time.Unix(int64(100-step), 0)
					default:
						p = appstate.BuildLabelChat(chat, "7", step == 0)
						p.Timestamp = time.Unix(int64(100-step), 0)
					}
					var batch []any
					if boundary {
						batch, err = sendRegularAppState(t.Context(), s, p)
					} else {
						err = s.send(t.Context(), p)
					}
					if err != nil {
						t.Fatal(err)
					}
					// The app commits its optimistic value, then the captured SDK
					// result, under the already-reserved persistence ticket.
					var local []any
					if err := c.client.DangerousInternals().CollectEventsToDispatch(t.Context(), p.Type, []appstate.Mutation{{Operation: waServerSync.SyncdMutation_SET, Index: p.Mutations[0].Index, Action: p.Mutations[0].Value}}, false, &local); err != nil {
						t.Fatal(err)
					}
					persist(local)
					persist(batch)
				}
				if s.late != nil {
					s.late()
				}
				want := "B"
				if !boundary {
					want = "A"
				}
				switch kind {
				case "reply-edit", "reply-delete":
					q, _, err := db.QuickReplyByID("1")
					if err != nil || q.Message != want || q.Deleted != (boundary && kind == "reply-delete") {
						t.Fatalf("reply=%+v %v; want %s, deleted=%t", q, err, want, boundary && kind == "reply-delete")
					}
				case "label-edit", "label-delete":
					l, _, err := db.ListByID("7")
					if err != nil || l.Name != want || l.Deleted != (boundary && kind == "label-delete") {
						t.Fatalf("label=%+v %v; want %s", l, err, want)
					}
				default:
					rows, err := db.ListChatsFiltered(appStore.ChatListFilter{ListID: "7"})
					if err != nil || len(rows) != map[bool]int{true: 0, false: 1}[boundary] {
						t.Fatalf("members=%+v %v", rows, err)
					}
				}
			})
		}
	}
}

func TestRegularSend409KeepsSDKApplicationOrderAndFailedSendEvents(t *testing.T) {
	for _, failure := range []string{"success", "retry rejected", "fetch failed"} {
		t.Run(failure, func(t *testing.T) {
			c, err := New(Options{StorePath: newPairedSessionStore(t)})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			id := []byte{1, 2, 3, 4}
			if err := c.client.Store.AppStateKeys.PutAppStateSyncKey(t.Context(), id, waStore.AppStateSyncKey{Data: bytes.Repeat([]byte{7}, 32), Fingerprint: []byte{1}, Timestamp: 1}); err != nil {
				t.Fatal(err)
			}
			s := &delayedRegularSDK{sdkRegularAppStateSender: sdkRegularAppStateSender{cli: c.client}}
			p := appstate.PatchInfo{Type: appstate.WAPatchRegular, Mutations: []appstate.MutationInfo{{Index: []string{appstate.IndexQuickReply, "1"}, Version: 3, Value: &waSyncAction.SyncActionValue{QuickReplyAction: &waSyncAction.QuickReplyAction{Message: proto.String("conflict")}}}}}
			raw, err := s.encode(t.Context(), appstate.HashState{}, p)
			if err != nil {
				t.Fatal(err)
			}
			conflict := &waServerSync.SyncdPatch{}
			if err := proto.Unmarshal(raw, conflict); err != nil {
				t.Fatal(err)
			}
			conflict.Version = &waServerSync.SyncdVersion{Version: proto.Uint64(1)}
			raw, err = proto.Marshal(conflict)
			if err != nil {
				t.Fatal(err)
			}
			reject := func(code int, patch []byte) *waBinary.Node {
				return &waBinary.Node{Tag: "iq", Content: []waBinary.Node{{Tag: "sync", Content: []waBinary.Node{{Tag: "collection", Attrs: waBinary.Attrs{"name": "regular", "type": "error"}, Content: []waBinary.Node{{Tag: "error", Attrs: waBinary.Attrs{"code": fmt.Sprint(code)}}, {Tag: "patches", Content: []waBinary.Node{{Tag: "patch", Content: patch}}}}}}}}}
			}
			s.responses = []*waBinary.Node{reject(409, raw)}
			if failure == "retry rejected" {
				s.responses = append(s.responses, reject(409, raw))
			}
			if failure == "fetch failed" {
				s.fetchError = errors.New("injected fetch failure")
			}
			p.Mutations[0].Value.QuickReplyAction.Message = proto.String("accepted retry")
			batch, err := sendRegularAppState(t.Context(), s, p)
			if (err != nil) != (failure != "success") || s.requests != 2 {
				t.Fatalf("requests=%d error=%v", s.requests, err)
			}
			if failure == "retry rejected" && !errors.Is(err, whatsmeow.ErrAppStateUpdate) {
				t.Fatalf("lost SDK error: %v", err)
			}
			want := []string{"conflict"}
			if failure == "success" {
				want = append(want, "accepted retry")
			}
			if len(batch) != len(want) {
				t.Fatalf("batch=%v, want %v", batch, want)
			}
			for i, evt := range batch {
				if got := evt.(*events.AppState).GetQuickReplyAction().GetMessage(); got != want[i] {
					t.Fatalf("event %d=%q want %q", i, got, want[i])
				}
			}
		})
	}
}

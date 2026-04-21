package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/steipete/wacli/internal/wa"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

type fakeWA struct {
	mu sync.Mutex

	authed    bool
	connected bool

	nextHandlerID uint32
	handlers      map[uint32]func(interface{})

	connectEvents []interface{}
	connectDelay  time.Duration
	downloadDelay time.Duration

	contacts map[types.JID]types.ContactInfo
	groups   map[types.JID]*types.GroupInfo

	onDemandHistory func(lastKnown types.MessageInfo, count int) *events.HistorySync
}

func newFakeWA() *fakeWA {
	return &fakeWA{
		authed:        true,
		handlers:      map[uint32]func(interface{}){},
		contacts:      map[types.JID]types.ContactInfo{},
		groups:        map[types.JID]*types.GroupInfo{},
		nextHandlerID: 1,
	}
}

func (f *fakeWA) emit(evt interface{}) {
	f.mu.Lock()
	handlers := make([]func(interface{}), 0, len(f.handlers))
	for _, h := range f.handlers {
		handlers = append(handlers, h)
	}
	f.mu.Unlock()
	for _, h := range handlers {
		h(evt)
	}
}

func (f *fakeWA) Close() { f.mu.Lock(); f.connected = false; f.mu.Unlock() }

func (f *fakeWA) IsAuthed() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.authed }
func (f *fakeWA) IsConnected() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connected
}

func (f *fakeWA) Connect(ctx context.Context, opts wa.ConnectOptions) error {
	f.mu.Lock()
	authed := f.authed
	f.connected = true
	eventsToEmit := append([]interface{}{}, f.connectEvents...)
	f.mu.Unlock()

	if !authed && !opts.AllowQR {
		return fmt.Errorf("not authenticated; run `wacli auth`")
	}
	if f.connectDelay > 0 {
		select {
		case <-time.After(f.connectDelay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.emit(&events.Connected{})
	for _, e := range eventsToEmit {
		f.emit(e)
	}
	return nil
}

func (f *fakeWA) AddEventHandler(handler func(interface{})) uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.nextHandlerID
	f.nextHandlerID++
	f.handlers[id] = handler
	return id
}

func (f *fakeWA) RemoveEventHandler(id uint32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.handlers, id)
}

func (f *fakeWA) ReconnectWithBackoff(ctx context.Context, minDelay, maxDelay time.Duration) error {
	return f.Connect(ctx, wa.ConnectOptions{AllowQR: false})
}

func (f *fakeWA) ResolveChatName(ctx context.Context, chat types.JID, pushName string) string {
	if pushName != "" && pushName != "-" {
		return pushName
	}
	if chat.Server == types.GroupServer {
		if gi, _ := f.GetGroupInfo(ctx, chat); gi != nil && gi.GroupName.Name != "" {
			return gi.GroupName.Name
		}
	}
	if info, _ := f.GetContact(ctx, chat.ToNonAD()); info.Found {
		if name := wa.BestContactName(info); name != "" {
			return name
		}
	}
	return chat.String()
}

func (f *fakeWA) GetContact(ctx context.Context, jid types.JID) (types.ContactInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v, ok := f.contacts[jid]; ok {
		return v, nil
	}
	return types.ContactInfo{Found: false}, nil
}

func (f *fakeWA) GetAllContacts(ctx context.Context) (map[types.JID]types.ContactInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[types.JID]types.ContactInfo, len(f.contacts))
	for k, v := range f.contacts {
		out[k] = v
	}
	return out, nil
}

func (f *fakeWA) GetJoinedGroups(ctx context.Context) ([]*types.GroupInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*types.GroupInfo, 0, len(f.groups))
	for _, g := range f.groups {
		out = append(out, g)
	}
	return out, nil
}

func (f *fakeWA) GetGroupInfo(ctx context.Context, jid types.JID) (*types.GroupInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.groups[jid], nil
}

func (f *fakeWA) SetGroupName(ctx context.Context, jid types.JID, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	g := f.groups[jid]
	if g == nil {
		g = &types.GroupInfo{JID: jid}
		f.groups[jid] = g
	}
	g.GroupName.Name = name
	return nil
}

func (f *fakeWA) UpdateGroupParticipants(ctx context.Context, group types.JID, users []types.JID, action wa.GroupParticipantAction) ([]types.GroupParticipant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	g := f.groups[group]
	if g == nil {
		g = &types.GroupInfo{JID: group}
		f.groups[group] = g
	}
	switch action {
	case wa.GroupParticipantAdd:
		for _, u := range users {
			g.Participants = append(g.Participants, types.GroupParticipant{JID: u})
		}
	case wa.GroupParticipantRemove:
		var kept []types.GroupParticipant
		rm := map[types.JID]bool{}
		for _, u := range users {
			rm[u] = true
		}
		for _, p := range g.Participants {
			if !rm[p.JID] {
				kept = append(kept, p)
			}
		}
		g.Participants = kept
	default:
		// promote/demote ignored for tests
	}
	return g.Participants, nil
}

func (f *fakeWA) GetGroupInviteLink(ctx context.Context, group types.JID, reset bool) (string, error) {
	return "https://chat.whatsapp.com/invite/test", nil
}

func (f *fakeWA) JoinGroupWithLink(ctx context.Context, code string) (types.JID, error) {
	return types.ParseJID("12345@g.us")
}

func (f *fakeWA) LeaveGroup(ctx context.Context, group types.JID) error { return nil }

func (f *fakeWA) SendText(ctx context.Context, to types.JID, text string) (types.MessageID, error) {
	return types.MessageID("msgid"), nil
}

func (f *fakeWA) SendProtoMessage(ctx context.Context, to types.JID, msg *waProto.Message) (types.MessageID, error) {
	return types.MessageID("msgid"), nil
}

func (f *fakeWA) Upload(ctx context.Context, data []byte, mediaType whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
	return whatsmeow.UploadResponse{}, nil
}

func (f *fakeWA) SendChatPresence(ctx context.Context, jid types.JID, state types.ChatPresence, media types.ChatPresenceMedia) error {
	return nil
}

func (f *fakeWA) DecryptReaction(ctx context.Context, reaction *events.Message) (*waProto.ReactionMessage, error) {
	return nil, fmt.Errorf("not supported")
}

func (f *fakeWA) DownloadMediaToFile(ctx context.Context, directPath string, encFileHash, fileHash, mediaKey []byte, fileLength uint64, mediaType, mmsType string, targetPath string) (int64, error) {
	if f.downloadDelay > 0 {
		select {
		case <-time.After(f.downloadDelay):
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o700); err != nil {
		return 0, err
	}
	if err := os.WriteFile(targetPath, []byte("test"), 0o600); err != nil {
		return 0, err
	}
	st, err := os.Stat(targetPath)
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

func (f *fakeWA) RequestHistorySyncOnDemand(ctx context.Context, lastKnown types.MessageInfo, count int) (types.MessageID, error) {
	f.mu.Lock()
	cb := f.onDemandHistory
	f.mu.Unlock()
	if cb != nil {
		f.emit(cb(lastKnown, count))
	}
	return types.MessageID("req"), nil
}

func (f *fakeWA) Logout(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.authed = false
	return nil
}

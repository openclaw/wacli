package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/openclaw/wacli/internal/fsutil"
	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	waStore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

type fakeWA struct {
	mu sync.Mutex

	authed        bool
	connected     bool
	autoReconnect bool
	linkedLID     string

	nextHandlerID uint32
	handlers      map[uint32]func(any)

	connectEvents  []any
	connectErrs    []error
	connectCalls   int
	connectDelay   time.Duration
	connectStarted chan struct{}
	downloadDelay  time.Duration
	downloadErr    error

	onMediaRetry       func(info *types.MessageInfo, mediaKey []byte) any
	mediaRetryReceipts []string

	contacts map[types.JID]types.ContactInfo
	// journal is the real wa.ContactStoreJournal over the fake contact store,
	// as session.go installs it over whatsmeow's; applyContactBatch writes
	// through it the way dispatchAppState does. contactStoreErr makes the
	// inner store fail for a JID while the write is still journaled.
	journal         *wa.ContactStoreJournal
	contactStoreErr map[types.JID]error
	groups          map[types.JID]*types.GroupInfo
	news            map[types.JID]*types.NewsletterMetadata
	lids            map[types.JID]types.JID

	groupInfoCalls int
	groupInfoErr   error
	onGroupInfo    func() // runs while a group info lookup is in flight

	getAllContactsErr           error
	getJoinedGroupsErr          error
	getSubscribedNewslettersErr error
	decryptedReaction           *waProto.ReactionMessage
	decryptReactionErr          error
	sendPollCalls               []fakeSendPollCall
	sendPollVoteCalls           []fakeSendPollVoteCall
	decryptPollVoteFunc         func(evt *events.Message) (*waE2E.PollVoteMessage, error)
	decryptSecretFunc           func(evt *events.Message) (*waE2E.Message, error)
	onDemandHistory             func(lastKnown types.MessageInfo, count int) *events.HistorySync
	onDemandEvent               func(lastKnown types.MessageInfo, count int) any
	onDemandErr                 error
	downloadHistory             func(notif *waE2E.HistorySyncNotification) (*waHistorySync.HistorySync, error)
	deleteHistoryCalls          []*waE2E.HistorySyncNotification
	appStateRecoveryErr         error
	saveContactCalls            []fakeSaveContactCall
	saveContactErr              error
	saveContactAfterSend        func(call fakeSaveContactCall)
	// saveContactErrAfterSend fails the send after saveContactAfterSend ran,
	// like a 409 retry or a post-send fetch that fails after whatsmeow
	// already applied (and journaled) a batch.
	saveContactErrAfterSend error
	onAppStateRecovery      func(name string)
	appStateFetchErr        error
	appStateFetchErrs       []error
	appStateFetchEvent      func(name string, fullSync, onlyIfNotSynced bool) any
	archiveEvent            func() any
	archiveErr              error
	archiveCalls            []fakeArchiveCall
	pinCalls                []fakePinCall
	muteCalls               []fakeMuteCall
	markReadCalls           []fakeMarkReadCall
	markReadBeforeApply     func()
	markReadAfterApply      func()
	readReceiptCalls        []fakeReadReceiptCall
	onReadReceipt           func(int) (types.ReceiptType, error)
	readReceiptErr          error
	readReceiptType         types.ReceiptType
	manualHistorySyncCalls  []bool
	appStateRecoveries      []string
	appStateFetches         []fakeAppStateFetch
	hasLocalAppState        bool

	presenceCalls   []types.Presence
	sendPresenceErr error

	presenceSubscriptions []types.JID
	subscribePresenceErr  error
	// presenceSubscribeAttempts records every call, failed ones included;
	// slowSubscribePresence, when set, runs first and can stall or fail one.
	presenceSubscribeAttempts []types.JID
	slowSubscribePresence     func(context.Context, types.JID) error
	// onSubscribePresence, when set, returns an event emitted as WhatsApp's
	// answer before SubscribePresence returns (nil emits nothing).
	onSubscribePresence func(types.JID) any
}

type fakeArchiveCall struct {
	target     types.JID
	archive    bool
	lastMsgTS  time.Time
	lastMsgKey *waCommon.MessageKey
}

type fakePinCall struct {
	target types.JID
	pin    bool
}

type fakeMuteCall struct {
	target   types.JID
	mute     bool
	duration time.Duration
}

type fakeMarkReadCall struct {
	target     types.JID
	read       bool
	lastMsgTS  time.Time
	lastMsgKey *waCommon.MessageKey
}

type fakeReadReceiptCall struct {
	ids        []types.MessageID
	timestamp  time.Time
	chat       types.JID
	sender     types.JID
	addressing types.AddressingMode
}

type fakeSendPollCall struct {
	to         types.JID
	name       string
	options    []string
	selectable int
	ephemeral  bool
}

type fakeSendPollVoteCall struct {
	pollInfo types.MessageInfo
	options  []string
}

type fakeAppStateFetch struct {
	name            string
	fullSync        bool
	onlyIfNotSynced bool
}

func newFakeWA() *fakeWA {
	f := &fakeWA{
		authed:          true,
		autoReconnect:   true,
		handlers:        map[uint32]func(any){},
		contacts:        map[types.JID]types.ContactInfo{},
		contactStoreErr: map[types.JID]error{},
		groups:          map[types.JID]*types.GroupInfo{},
		news:            map[types.JID]*types.NewsletterMetadata{},
		lids:            map[types.JID]types.JID{},
		nextHandlerID:   1,
	}
	f.journal = wa.NewContactStoreJournal(&fakeContactStore{f: f})
	return f
}

func (f *fakeWA) ContactJournal() *wa.ContactStoreJournal { return f.journal }

// fakeContactStore is the whatsmeow contact store of the fake: independent
// rows per raw JID (phone number or LID), names overwritten as a unit, like
// sqlstore.PutContactName. Argument order follows the sqlstore implementation
// and the SDK caller: (firstName, fullName).
type fakeContactStore struct{ f *fakeWA }

func (s *fakeContactStore) PutContactName(ctx context.Context, user types.JID, firstName, fullName string) error {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	if err := s.f.contactStoreErr[user]; err != nil {
		return err
	}
	info := s.f.contacts[user]
	info.Found = true
	info.FirstName = firstName
	info.FullName = fullName
	s.f.contacts[user] = info
	return nil
}

func (s *fakeContactStore) PutAllContactNames(ctx context.Context, contacts []waStore.ContactEntry) error {
	for _, c := range contacts {
		if err := s.PutContactName(ctx, c.JID, c.FirstName, c.FullName); err != nil {
			return err
		}
	}
	return nil
}

func (s *fakeContactStore) PutPushName(ctx context.Context, user types.JID, pushName string) (bool, string, error) {
	return false, "", nil
}

func (s *fakeContactStore) PutBusinessName(ctx context.Context, user types.JID, businessName string) (bool, string, error) {
	return false, "", nil
}

func (s *fakeContactStore) PutManyRedactedPhones(ctx context.Context, entries []waStore.RedactedPhoneEntry) error {
	return nil
}

func (s *fakeContactStore) GetContact(ctx context.Context, user types.JID) (types.ContactInfo, error) {
	return s.f.GetContact(ctx, user)
}

func (s *fakeContactStore) GetAllContacts(ctx context.Context) (map[types.JID]types.ContactInfo, error) {
	return s.f.GetAllContacts(ctx)
}

func (f *fakeWA) emit(evt any) {
	f.mu.Lock()
	ids := make([]uint32, 0, len(f.handlers))
	for id := range f.handlers {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	handlers := make([]func(any), 0, len(ids))
	for _, id := range ids {
		handlers = append(handlers, f.handlers[id])
	}
	f.mu.Unlock()
	for _, h := range handlers {
		h(evt)
	}
}

func (f *fakeWA) Close()      { f.Disconnect() }
func (f *fakeWA) Disconnect() { f.mu.Lock(); f.connected = false; f.mu.Unlock() }

func (f *fakeWA) IsAuthed() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.authed }
func (f *fakeWA) IsConnected() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connected
}

func (f *fakeWA) SetAutoReconnect(enabled bool) (bool, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	previous := f.autoReconnect
	if f.connected {
		return previous, false
	}
	f.autoReconnect = enabled
	return previous, true
}

func (f *fakeWA) Connect(ctx context.Context, opts wa.ConnectOptions) error {
	f.mu.Lock()
	if f.connected {
		f.mu.Unlock()
		return nil
	}
	f.connectCalls++
	if f.connectStarted != nil {
		select {
		case f.connectStarted <- struct{}{}:
		default:
		}
	}
	authed := f.authed
	var connectErr error
	if len(f.connectErrs) > 0 {
		connectErr = f.connectErrs[0]
		f.connectErrs = f.connectErrs[1:]
	}
	f.connected = true
	eventsToEmit := append([]any{}, f.connectEvents...)
	f.mu.Unlock()

	if !authed && !opts.AllowQR {
		f.mu.Lock()
		f.connected = false
		f.mu.Unlock()
		return fmt.Errorf("not authenticated; run `wacli auth`")
	}
	if connectErr != nil {
		f.mu.Lock()
		f.connected = false
		f.mu.Unlock()
		return connectErr
	}
	if f.connectDelay > 0 {
		select {
		case <-time.After(f.connectDelay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if authed && !opts.SuppressInitialAvailablePresence {
		_ = f.SendPresence(ctx, types.PresenceAvailable)
	}
	f.emit(&events.Connected{})
	for _, e := range eventsToEmit {
		f.emit(e)
	}
	return nil
}

func (f *fakeWA) AddEventHandler(handler func(any)) uint32 {
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

func (f *fakeWA) ReconnectWithBackoff(ctx context.Context, minDelay, maxDelay time.Duration, opts wa.ConnectOptions) error {
	opts.AllowQR = false
	return f.Connect(ctx, opts)
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
	if chat.Server == types.NewsletterServer {
		if meta, _ := f.GetNewsletterInfo(ctx, chat); meta != nil {
			if name := wa.NewsletterName(meta); name != "" {
				return name
			}
		}
	}
	if info, _ := f.GetContact(ctx, chat.ToNonAD()); info.Found {
		if name := wa.BestContactName(info); name != "" {
			return name
		}
	}
	return chat.String()
}

func (f *fakeWA) ResolveLIDToPN(ctx context.Context, jid types.JID) types.JID {
	f.mu.Lock()
	defer f.mu.Unlock()
	if pn, ok := f.lids[jid.ToNonAD()]; ok {
		pn.Device = jid.Device
		return pn
	}
	return jid
}

func (f *fakeWA) ResolvePNToLID(ctx context.Context, jid types.JID) types.JID {
	f.mu.Lock()
	defer f.mu.Unlock()
	for lid, pn := range f.lids {
		if pn == jid.ToNonAD() {
			lid.Device = jid.Device
			return lid
		}
	}
	return jid
}

func (f *fakeWA) GetUserInfo(ctx context.Context, jids []types.JID) (map[types.JID]types.UserInfo, error) {
	return map[types.JID]types.UserInfo{}, nil
}

func (f *fakeWA) IsOnWhatsApp(ctx context.Context, phones []string) ([]types.IsOnWhatsAppResponse, error) {
	return nil, nil
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
	if f.getAllContactsErr != nil {
		return nil, f.getAllContactsErr
	}
	out := make(map[types.JID]types.ContactInfo, len(f.contacts))
	for k, v := range f.contacts {
		out[k] = v
	}
	return out, nil
}

func (f *fakeWA) GetJoinedGroups(ctx context.Context) ([]*types.GroupInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getJoinedGroupsErr != nil {
		return nil, f.getJoinedGroupsErr
	}
	out := make([]*types.GroupInfo, 0, len(f.groups))
	for _, g := range f.groups {
		out = append(out, g)
	}
	return out, nil
}

func (f *fakeWA) GetGroupInfo(ctx context.Context, jid types.JID) (*types.GroupInfo, error) {
	f.mu.Lock()
	f.groupInfoCalls++
	hook := f.onGroupInfo
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.groupInfoErr != nil {
		return nil, f.groupInfoErr
	}
	return f.groups[jid], nil
}

func (f *fakeWA) CreateGroup(ctx context.Context, req wa.CreateGroupRequest) (*types.GroupInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	jid := types.NewJID(fmt.Sprintf("group-%d", len(f.groups)+1), types.GroupServer)
	g := &types.GroupInfo{
		JID:           jid,
		GroupName:     types.GroupName{Name: req.Name},
		GroupAnnounce: types.GroupAnnounce{IsAnnounce: req.IsAnnounce},
		GroupLocked:   types.GroupLocked{IsLocked: req.IsLocked},
		GroupMembershipApprovalMode: types.GroupMembershipApprovalMode{
			IsJoinApprovalRequired: req.IsJoinApprovalRequired,
		},
		GroupParent:       types.GroupParent{IsParent: req.IsParent},
		GroupLinkedParent: types.GroupLinkedParent{LinkedParentJID: req.LinkedParentJID},
	}
	for _, p := range req.Participants {
		g.Participants = append(g.Participants, types.GroupParticipant{JID: p})
	}
	f.groups[jid] = g
	return g, nil
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

func (f *fakeWA) SetGroupTopic(ctx context.Context, jid types.JID, topic string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	g := f.groups[jid]
	if g == nil {
		g = &types.GroupInfo{JID: jid}
		f.groups[jid] = g
	}
	g.GroupTopic.Topic = topic
	return nil
}

func (f *fakeWA) SetGroupAnnounce(ctx context.Context, jid types.JID, announce bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	g := f.groups[jid]
	if g == nil {
		g = &types.GroupInfo{JID: jid}
		f.groups[jid] = g
	}
	g.GroupAnnounce.IsAnnounce = announce
	return nil
}

func (f *fakeWA) SetGroupLocked(ctx context.Context, jid types.JID, locked bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	g := f.groups[jid]
	if g == nil {
		g = &types.GroupInfo{JID: jid}
		f.groups[jid] = g
	}
	g.GroupLocked.IsLocked = locked
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

func (f *fakeWA) GetGroupRequestParticipants(ctx context.Context, group types.JID) ([]types.GroupParticipantRequest, error) {
	return nil, nil
}

func (f *fakeWA) UpdateGroupRequestParticipants(ctx context.Context, group types.JID, users []types.JID, action wa.GroupParticipantRequestAction) ([]types.GroupParticipant, error) {
	out := make([]types.GroupParticipant, 0, len(users))
	for _, u := range users {
		out = append(out, types.GroupParticipant{JID: u})
	}
	return out, nil
}

func (f *fakeWA) GetGroupInviteLink(ctx context.Context, group types.JID, reset bool) (string, error) {
	return "https://chat.whatsapp.com/invite/test", nil
}

func (f *fakeWA) JoinGroupWithLink(ctx context.Context, code string) (types.JID, error) {
	return types.ParseJID("12345@g.us")
}

func (f *fakeWA) LeaveGroup(ctx context.Context, group types.JID) error { return nil }

func (f *fakeWA) GetNewsletterInfoWithInvite(ctx context.Context, key string) (*types.NewsletterMetadata, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, meta := range f.news {
		if meta.ThreadMeta.InviteCode == key || strings.HasSuffix(key, meta.ThreadMeta.InviteCode) {
			return meta, nil
		}
	}
	return nil, nil
}

func (f *fakeWA) FollowNewsletter(ctx context.Context, jid types.JID) error { return nil }

func (f *fakeWA) UnfollowNewsletter(ctx context.Context, jid types.JID) error { return nil }

func (f *fakeWA) GetSubscribedNewsletters(ctx context.Context) ([]*types.NewsletterMetadata, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getSubscribedNewslettersErr != nil {
		return nil, f.getSubscribedNewslettersErr
	}
	out := make([]*types.NewsletterMetadata, 0, len(f.news))
	for _, meta := range f.news {
		out = append(out, meta)
	}
	return out, nil
}

func (f *fakeWA) GetNewsletterInfo(ctx context.Context, jid types.JID) (*types.NewsletterMetadata, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.news[jid], nil
}

func (f *fakeWA) SendText(ctx context.Context, to types.JID, text string) (types.MessageID, error) {
	return types.MessageID("msgid"), nil
}

func (f *fakeWA) SendProtoMessage(ctx context.Context, to types.JID, msg *waProto.Message) (types.MessageID, error) {
	return f.SendProtoMessageWithExtra(ctx, to, msg, "")
}

func (f *fakeWA) SendProtoMessageWithExtra(ctx context.Context, to types.JID, msg *waProto.Message, mediaHandle string) (types.MessageID, error) {
	return types.MessageID("msgid"), nil
}

func (f *fakeWA) SendReaction(ctx context.Context, chat, sender types.JID, targetID types.MessageID, reaction string) (types.MessageID, error) {
	return types.MessageID("reactionid"), nil
}

func (f *fakeWA) SendPoll(ctx context.Context, to types.JID, name string, options []string, selectable int, ephemeral bool) (types.MessageID, error) {
	f.mu.Lock()
	f.sendPollCalls = append(f.sendPollCalls, fakeSendPollCall{
		to:         to,
		name:       name,
		options:    append([]string(nil), options...),
		selectable: selectable,
		ephemeral:  ephemeral,
	})
	f.mu.Unlock()
	return types.MessageID("pollid"), nil
}

func (f *fakeWA) SendPollVote(ctx context.Context, pollInfo *types.MessageInfo, options []string) (types.MessageID, error) {
	if pollInfo == nil {
		return "", fmt.Errorf("poll info required")
	}
	f.mu.Lock()
	f.sendPollVoteCalls = append(f.sendPollVoteCalls, fakeSendPollVoteCall{
		pollInfo: *pollInfo,
		options:  append([]string(nil), options...),
	})
	f.mu.Unlock()
	return types.MessageID("pollvoteid"), nil
}

func (f *fakeWA) DecryptPollVote(ctx context.Context, evt *events.Message) (*waE2E.PollVoteMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	cb := f.decryptPollVoteFunc
	f.mu.Unlock()
	if cb != nil {
		return cb(evt)
	}
	return nil, fmt.Errorf("not supported")
}

func (f *fakeWA) DecryptSecretEncryptedMessage(ctx context.Context, evt *events.Message) (*waE2E.Message, error) {
	f.mu.Lock()
	cb := f.decryptSecretFunc
	f.mu.Unlock()
	if cb != nil {
		return cb(evt)
	}
	return nil, fmt.Errorf("not supported")
}

func (f *fakeWA) RevokeMessage(ctx context.Context, chat, sender types.JID, targetID types.MessageID) (types.MessageID, error) {
	return types.MessageID("revokeid"), nil
}

func (f *fakeWA) EditMessage(ctx context.Context, chat types.JID, targetID types.MessageID, text string) (types.MessageID, error) {
	return types.MessageID("editid"), nil
}

func (f *fakeWA) Upload(ctx context.Context, data []byte, mediaType whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
	return whatsmeow.UploadResponse{}, nil
}

func (f *fakeWA) UploadNewsletter(ctx context.Context, data []byte, mediaType whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
	return whatsmeow.UploadResponse{Handle: "newsletter-media-handle"}, nil
}

func (f *fakeWA) SendChatPresence(ctx context.Context, jid types.JID, state types.ChatPresence, media types.ChatPresenceMedia) error {
	return nil
}

func (f *fakeWA) SendPresence(ctx context.Context, presence types.Presence) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.presenceCalls = append(f.presenceCalls, presence)
	return f.sendPresenceErr
}

func (f *fakeWA) SubscribePresence(ctx context.Context, jid types.JID) error {
	f.mu.Lock()
	f.presenceSubscribeAttempts = append(f.presenceSubscribeAttempts, jid)
	slow := f.slowSubscribePresence
	f.mu.Unlock()
	if slow != nil {
		if err := slow(ctx, jid); err != nil {
			return err
		}
	}
	f.mu.Lock()
	if f.subscribePresenceErr != nil {
		err := f.subscribePresenceErr
		f.mu.Unlock()
		return err
	}
	f.presenceSubscriptions = append(f.presenceSubscriptions, jid)
	hook := f.onSubscribePresence
	f.mu.Unlock()
	if hook != nil {
		if evt := hook(jid); evt != nil {
			f.emit(evt)
		}
	}
	return nil
}

func (f *fakeWA) DecryptReaction(ctx context.Context, reaction *events.Message) (*waProto.ReactionMessage, error) {
	if f.decryptReactionErr != nil {
		return nil, f.decryptReactionErr
	}
	if f.decryptedReaction != nil {
		return f.decryptedReaction, nil
	}
	return nil, fmt.Errorf("not supported")
}

func (f *fakeWA) SetManualHistorySyncDownload(enabled bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.manualHistorySyncCalls = append(f.manualHistorySyncCalls, enabled)
}

func (f *fakeWA) DownloadHistorySync(ctx context.Context, notif *waE2E.HistorySyncNotification) (*waHistorySync.HistorySync, error) {
	f.mu.Lock()
	cb := f.downloadHistory
	f.mu.Unlock()
	if cb == nil {
		return nil, fmt.Errorf("not supported")
	}
	return cb(notif)
}

func (f *fakeWA) DeleteHistorySyncMedia(ctx context.Context, notif *waE2E.HistorySyncNotification) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteHistoryCalls = append(f.deleteHistoryCalls, notif)
	return nil
}

func (f *fakeWA) ParseWebMessage(chatJID types.JID, webMsg *waWeb.WebMessageInfo) (*events.Message, error) {
	if chatJID.IsEmpty() {
		parsed, err := types.ParseJID(webMsg.GetKey().GetRemoteJID())
		if err != nil {
			return nil, err
		}
		chatJID = parsed
	}
	sender := chatJID
	if webMsg.GetKey().GetFromMe() {
		if linked, err := types.ParseJID(f.LinkedJID()); err == nil {
			sender = linked
		}
	} else if participant := webMsg.GetParticipant(); participant != "" {
		parsed, err := types.ParseJID(participant)
		if err != nil {
			return nil, err
		}
		sender = parsed
	}
	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:     chatJID,
				Sender:   sender,
				IsFromMe: webMsg.GetKey().GetFromMe(),
				IsGroup:  chatJID.Server == types.GroupServer,
			},
			ID:        webMsg.GetKey().GetID(),
			Timestamp: time.Unix(int64(webMsg.GetMessageTimestamp()), 0).UTC(),
		},
		RawMessage: webMsg.GetMessage(),
	}
	evt.UnwrapRaw()
	return evt, nil
}

func (f *fakeWA) DownloadMediaToFile(ctx context.Context, directPath string, encFileHash, fileHash, mediaKey []byte, fileLength uint64, mediaType, mmsType string, targetPath string) (int64, error) {
	if f.downloadDelay > 0 {
		select {
		case <-time.After(f.downloadDelay):
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	if f.downloadErr != nil {
		return 0, f.downloadErr
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o700); err != nil {
		return 0, err
	}
	if err := fsutil.WritePrivateFile(targetPath, []byte("test")); err != nil {
		return 0, err
	}
	st, err := os.Stat(targetPath)
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

func (f *fakeWA) SendMediaRetryReceipt(ctx context.Context, info *types.MessageInfo, mediaKey []byte) error {
	f.mu.Lock()
	f.mediaRetryReceipts = append(f.mediaRetryReceipts, string(info.ID))
	hook := f.onMediaRetry
	f.mu.Unlock()
	if hook != nil {
		if evt := hook(info, mediaKey); evt != nil {
			f.emit(evt)
		}
	}
	return nil
}

func (f *fakeWA) MarkRead(ctx context.Context, ids []types.MessageID, timestamp time.Time, chat, sender types.JID, addressing types.AddressingMode) (types.ReceiptType, error) {
	f.mu.Lock()
	if f.readReceiptErr != nil {
		err := f.readReceiptErr
		f.mu.Unlock()
		return "", err
	}
	f.readReceiptCalls = append(f.readReceiptCalls, fakeReadReceiptCall{ids: append([]types.MessageID(nil), ids...), timestamp: timestamp, chat: chat, sender: sender, addressing: addressing})
	index := len(f.readReceiptCalls) - 1
	hook, kind := f.onReadReceipt, f.readReceiptType
	f.mu.Unlock()
	if hook != nil {
		return hook(index)
	}
	if kind != "" {
		return kind, nil
	}
	return wa.ReadReceiptUnknown, nil
}

func (f *fakeWA) RequestHistorySyncOnDemand(ctx context.Context, lastKnown types.MessageInfo, count int) (types.MessageID, error) {
	f.mu.Lock()
	if f.onDemandErr != nil {
		err := f.onDemandErr
		f.mu.Unlock()
		return "", err
	}
	eventCB := f.onDemandEvent
	cb := f.onDemandHistory
	f.mu.Unlock()
	if eventCB != nil {
		if evt := eventCB(lastKnown, count); evt != nil {
			f.emit(evt)
		}
	} else if cb != nil {
		if evt := cb(lastKnown, count); evt != nil {
			f.emit(evt)
		}
	}
	return types.MessageID("req"), nil
}

func (f *fakeWA) HasLocalAppState(ctx context.Context, name string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hasLocalAppState, nil
}

// noteFullSyncResult models stock whatsmeow's destructive full sync:
// fetchAppState deletes the saved collection version before decoding, so a
// failed full sync leaves no local app state behind, and a verified one
// stores a fresh version.
func (f *fakeWA) noteFullSyncResult(fullSync bool, err error) {
	if !fullSync {
		return
	}
	f.mu.Lock()
	f.hasLocalAppState = err == nil
	f.mu.Unlock()
}

func (f *fakeWA) RequestAppStateRecovery(ctx context.Context, name string) (types.MessageID, error) {
	f.mu.Lock()
	if f.appStateRecoveryErr != nil {
		err := f.appStateRecoveryErr
		f.mu.Unlock()
		return "", err
	}
	f.appStateRecoveries = append(f.appStateRecoveries, name)
	hook := f.onAppStateRecovery
	f.mu.Unlock()
	if hook != nil {
		hook(name)
	}
	return types.MessageID("recovery-req"), nil
}

func (f *fakeWA) DeleteMessageForMe(ctx context.Context, info types.MessageInfo, deleteMedia bool) error {
	return nil
}

func (f *fakeWA) ArchiveChat(ctx context.Context, target types.JID, archive bool, lastMsgTS time.Time, lastMsgKey *waCommon.MessageKey, beforeApply func()) ([]any, error) {
	f.mu.Lock()
	f.archiveCalls = append(f.archiveCalls, fakeArchiveCall{target: target, archive: archive, lastMsgTS: lastMsgTS, lastMsgKey: lastMsgKey})
	eventCB := f.archiveEvent
	f.mu.Unlock()
	beforeApply()
	if eventCB != nil {
		if evt := eventCB(); evt != nil {
			return []any{evt}, f.archiveErr
		}
	}
	return nil, f.archiveErr
}

func (f *fakeWA) PinChat(ctx context.Context, target types.JID, pin bool, beforeApply func()) ([]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pinCalls = append(f.pinCalls, fakePinCall{target: target, pin: pin})
	beforeApply()
	return nil, nil
}

func (f *fakeWA) MuteChat(ctx context.Context, target types.JID, mute bool, duration time.Duration, beforeApply func()) ([]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.muteCalls = append(f.muteCalls, fakeMuteCall{target: target, mute: mute, duration: duration})
	beforeApply()
	return nil, nil
}

func (f *fakeWA) MarkChatAsRead(ctx context.Context, target types.JID, read bool, lastMsgTS time.Time, lastMsgKey *waCommon.MessageKey, beforeApply func()) ([]any, error) {
	f.mu.Lock()
	f.markReadCalls = append(f.markReadCalls, fakeMarkReadCall{target: target, read: read, lastMsgTS: lastMsgTS, lastMsgKey: lastMsgKey})
	hook := f.markReadBeforeApply
	afterApply := f.markReadAfterApply
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	beforeApply()
	if afterApply != nil {
		afterApply()
	}
	return nil, nil
}

func (f *fakeWA) FetchAppState(ctx context.Context, name string, fullSync, onlyIfNotSynced bool) error {
	f.mu.Lock()
	f.appStateFetches = append(f.appStateFetches, fakeAppStateFetch{
		name:            name,
		fullSync:        fullSync,
		onlyIfNotSynced: onlyIfNotSynced,
	})
	err := f.appStateFetchErr
	if len(f.appStateFetchErrs) > 0 {
		err = f.appStateFetchErrs[0]
		f.appStateFetchErrs = f.appStateFetchErrs[1:]
	}
	eventCB := f.appStateFetchEvent
	f.mu.Unlock()
	if err != nil {
		return err
	}
	if eventCB != nil {
		if evt := eventCB(name, fullSync, onlyIfNotSynced); evt != nil {
			f.emit(evt)
		}
	}
	return nil
}

func (f *fakeWA) FetchAppStateEvents(ctx context.Context, name string, fullSync, onlyIfNotSynced bool) ([]any, error) {
	f.mu.Lock()
	f.appStateFetches = append(f.appStateFetches, fakeAppStateFetch{
		name:            name,
		fullSync:        fullSync,
		onlyIfNotSynced: onlyIfNotSynced,
	})
	err := f.appStateFetchErr
	if len(f.appStateFetchErrs) > 0 {
		err = f.appStateFetchErrs[0]
		f.appStateFetchErrs = f.appStateFetchErrs[1:]
	}
	eventCB := f.appStateFetchEvent
	f.mu.Unlock()
	f.noteFullSyncResult(fullSync, err)
	if err != nil {
		if !errors.Is(err, appstate.ErrKeyNotFound) {
			f.emit(&events.AppStateSyncError{Name: appstate.WAPatchName(name), FullSync: fullSync, Error: err})
		}
		return nil, err
	}
	if eventCB == nil {
		return nil, nil
	}
	evt := eventCB(name, fullSync, onlyIfNotSynced)
	if evt == nil {
		return nil, nil
	}
	return []any{evt}, nil
}

func (f *fakeWA) Logout(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.authed = false
	return nil
}

func (f *fakeWA) SetProfilePicture(ctx context.Context, avatar []byte) (string, error) {
	return "pic-id-fake", nil
}

func (f *fakeWA) GetProfilePictureInfo(ctx context.Context, jid types.JID, preview bool, existingID string) (*types.ProfilePictureInfo, error) {
	return &types.ProfilePictureInfo{ID: "pic-id-fake", URL: "https://example.invalid/avatar.jpg", Type: "image"}, nil
}

func (f *fakeWA) SetStatusMessage(ctx context.Context, msg string) error {
	return nil
}

func (f *fakeWA) SetProfileName(ctx context.Context, name string) error {
	return nil
}

type fakeSaveContactCall struct {
	target, lid         types.JID
	fullName, firstName string
	phonebook           bool
}

// SendAppStatePatch models whatsmeow's SendAppState: the persistence boundary runs
// before the send, then afterSend models the post-send fetch, whose events
// whatsmeow dispatches from a goroutine before returning. Without afterSend,
// a successful send models the plain post-send fetch: whatsmeow fetches the
// collection from the version the patch was built on, so the batch holds our
// own mutation (indexed by the phone number, as BuildSaveContact emits it),
// which it writes to its contact store before SendAppState returns.
func (f *fakeWA) SendAppStatePatch(ctx context.Context, patch appstate.PatchInfo, beforeApply func()) ([]any, error) {
	action := patch.Mutations[0].Value.GetContactAction()
	target, _ := types.ParseJID(action.GetPnJID())
	lid, _ := types.ParseJID(action.GetLidJID())
	call := fakeSaveContactCall{target: target, lid: lid, fullName: action.GetFullName(), firstName: action.GetFirstName(), phonebook: action.GetSaveOnPrimaryAddressbook()}
	f.mu.Lock()
	f.saveContactCalls = append(f.saveContactCalls, call)
	err := f.saveContactErr
	errAfterSend := f.saveContactErrAfterSend
	afterSend := f.saveContactAfterSend
	f.mu.Unlock()
	beforeApply()
	if err != nil {
		return nil, err
	}
	if afterSend != nil {
		afterSend(call)
	} else {
		f.applyContactBatch(contactEvent(call.target, call.fullName, call.firstName))()
	}
	return nil, errAfterSend
}

// applyContactBatch models whatsmeow applying one fetched batch of contact
// mutations: dispatchAppState writes every name to the session contact store
// synchronously, inside fetchAppState (so before SendAppState returns), with
// PutContactName(ctx, jid, act.GetFirstName(), act.GetFullName()), and only
// the events are handed to a goroutine. The store is written now, through
// the real journal; the returned func dispatches the batch's events,
// whenever that goroutine runs.
func (f *fakeWA) applyContactBatch(evts ...*events.Contact) func() {
	// Each mutation is its own patch: DecodePatches stores every patch's
	// version before any name of the batch is applied.
	for range evts {
		f.journal.NoteCursorWrite(string(appstate.WAPatchCriticalUnblockLow))
	}
	batch := make([]any, 0, len(evts))
	for _, evt := range evts {
		_ = f.journal.PutContactName(context.Background(), evt.JID, evt.Action.GetFirstName(), evt.Action.GetFullName())
		batch = append(batch, evt)
	}
	return func() { f.dispatchAsync(batch...) }
}

// dispatchAsync delivers events from a goroutine, like whatsmeow's post-send
// dispatch, and returns once every handler has been called.
func (f *fakeWA) dispatchAsync(evts ...any) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, evt := range evts {
			f.emit(evt)
		}
	}()
	<-done
}

func (f *fakeWA) GetBusinessProfile(ctx context.Context, jid types.JID) (*types.BusinessProfile, error) {
	return &types.BusinessProfile{JID: jid}, nil
}

func (f *fakeWA) LinkedJID() string {
	if !f.IsAuthed() {
		return ""
	}
	return "1234567890@s.whatsapp.net"
}

func (f *fakeWA) LinkedLID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.authed {
		return ""
	}
	return f.linkedLID
}

func (f *fakeWA) DownloadRetriedMediaToFile(ctx context.Context, directPath string, fileHash, mediaKey []byte, fileLength uint64, mediaType, targetPath string) (int64, error) {
	return f.DownloadMediaToFile(ctx, directPath, nil, fileHash, mediaKey, fileLength, mediaType, "", targetPath)
}

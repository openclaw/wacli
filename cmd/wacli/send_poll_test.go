package main

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow/types"
)

type recordingPollSender struct {
	calls []recordingPollCall
}

type fakeOutboundPollIdentity struct {
	linked string
	group  *types.GroupInfo
	lid    types.JID
}

type recordingPollCall struct {
	to         types.JID
	name       string
	options    []string
	selectable int
	ephemeral  bool
}

func (r *recordingPollSender) SendPoll(_ context.Context, to types.JID, name string, options []string, selectable int, ephemeral bool) (types.MessageID, error) {
	r.calls = append(r.calls, recordingPollCall{
		to:         to,
		name:       name,
		options:    append([]string(nil), options...),
		selectable: selectable,
		ephemeral:  ephemeral,
	})
	return "pollid", nil
}

func (f fakeOutboundPollIdentity) LinkedJID() string {
	return f.linked
}

func (f fakeOutboundPollIdentity) ResolvePNToLID(_ context.Context, _ types.JID) types.JID {
	return f.lid
}

func (f fakeOutboundPollIdentity) GetGroupInfo(_ context.Context, _ types.JID) (*types.GroupInfo, error) {
	return f.group, nil
}

func TestValidatePollOptionsRequiresQuestion(t *testing.T) {
	if _, err := validatePollOptions("", []string{"a", "b"}, 1); err == nil {
		t.Fatal("expected error for empty question")
	}
}

func TestValidatePollOptionsTrimsAndRejectsDuplicates(t *testing.T) {
	if _, err := validatePollOptions("Q?", []string{"Yes", "  Yes  "}, 1); err == nil {
		t.Fatal("expected duplicate option error")
	}
	cleaned, err := validatePollOptions("Q?", []string{" Yes ", "No", ""}, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(cleaned, []string{"Yes", "No"}) {
		t.Fatalf("cleaned = %v", cleaned)
	}
}

func TestValidatePollOptionsBoundsCheck(t *testing.T) {
	if _, err := validatePollOptions("Q?", []string{"a"}, 1); err == nil {
		t.Fatal("expected error for <2 options")
	}
	tooMany := []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12", "13"}
	if _, err := validatePollOptions("Q?", tooMany, 1); err == nil {
		t.Fatal("expected error for >12 options")
	}
}

func TestValidatePollOptionsMultiBounds(t *testing.T) {
	if _, err := validatePollOptions("Q?", []string{"a", "b"}, 0); err == nil {
		t.Fatal("expected error for multi=0")
	}
	if _, err := validatePollOptions("Q?", []string{"a", "b"}, 3); err == nil {
		t.Fatal("expected error for multi > options")
	}
	if _, err := validatePollOptions("Q?", []string{"a", "b", "c"}, 2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSendPollMessageDispatchesSendPoll(t *testing.T) {
	rec := &recordingPollSender{}
	to := types.NewJID("15551234567", types.DefaultUserServer)
	id, err := sendPollMessage(context.Background(), rec, to, "Pizza?", []string{"Yes", "No"}, 1, false)
	if err != nil {
		t.Fatalf("sendPollMessage: %v", err)
	}
	if id != "pollid" {
		t.Fatalf("id = %q", id)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(rec.calls))
	}
	got := rec.calls[0]
	if got.name != "Pizza?" || !reflect.DeepEqual(got.options, []string{"Yes", "No"}) || got.selectable != 1 || got.ephemeral {
		t.Fatalf("unexpected call: %+v", got)
	}
	if got.to.String() != to.String() {
		t.Fatalf("to = %s", got.to)
	}
}

func TestSendPollMessageEphemeral(t *testing.T) {
	rec := &recordingPollSender{}
	to := types.NewJID("15551234567", types.DefaultUserServer)
	if _, err := sendPollMessage(context.Background(), rec, to, "Q?", []string{"a", "b"}, 2, true); err != nil {
		t.Fatalf("sendPollMessage: %v", err)
	}
	if !rec.calls[0].ephemeral {
		t.Fatal("expected ephemeral=true to flow through")
	}
	if rec.calls[0].selectable != 2 {
		t.Fatalf("selectable = %d", rec.calls[0].selectable)
	}
}

func TestOutboundPollSenderJIDUsesGroupLIDAddressing(t *testing.T) {
	group := types.NewJID("120363001234567890", types.GroupServer)
	pn := types.NewJID("15551234567", types.DefaultUserServer)
	lid := types.NewJID("999123456789", types.HiddenUserServer)
	wa := fakeOutboundPollIdentity{
		linked: pn.String(),
		group:  &types.GroupInfo{AddressingMode: types.AddressingModeLID},
		lid:    lid,
	}

	got := outboundPollSenderJID(context.Background(), wa, group)
	if got != lid.String() {
		t.Fatalf("sender = %q, want %q", got, lid.String())
	}
}

func TestOutboundPollSenderJIDKeepsPNForPNAddressedGroup(t *testing.T) {
	group := types.NewJID("120363001234567890", types.GroupServer)
	pn := types.NewJID("15551234567", types.DefaultUserServer)
	lid := types.NewJID("999123456789", types.HiddenUserServer)
	wa := fakeOutboundPollIdentity{
		linked: pn.String(),
		group:  &types.GroupInfo{AddressingMode: types.AddressingModePN},
		lid:    lid,
	}

	got := outboundPollSenderJID(context.Background(), wa, group)
	if got != pn.String() {
		t.Fatalf("sender = %q, want %q", got, pn.String())
	}
}

func TestRequirePollOptionsExist(t *testing.T) {
	if err := requirePollOptionsExist([]string{"Yes", "No"}, []string{"Yes"}); err != nil {
		t.Fatalf("expected ok: %v", err)
	}
	if err := requirePollOptionsExist([]string{"Yes", "No"}, []string{"Maybe"}); err == nil {
		t.Fatal("expected error for unknown option")
	}
}

func TestRequirePollSelectableCount(t *testing.T) {
	if err := requirePollSelectableCount(1, []string{"Yes"}); err != nil {
		t.Fatalf("expected ok: %v", err)
	}
	if err := requirePollSelectableCount(0, []string{"Yes", "No"}); err != nil {
		t.Fatalf("expected zero count to be unbounded: %v", err)
	}
	if err := requirePollSelectableCount(1, []string{"Yes", "No"}); err == nil {
		t.Fatal("expected error for over-selecting")
	}
}

func TestCleanVoteOptionsDedupAndTrim(t *testing.T) {
	cleaned, err := cleanVoteOptions([]string{"  A ", "B", "A", ""})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(cleaned, []string{"A", "B"}) {
		t.Fatalf("cleaned = %v", cleaned)
	}
	if _, err := cleanVoteOptions(nil); err == nil {
		t.Fatal("expected error for empty options")
	}
}

func TestBuildPollVoteInfoMarksGroupMessages(t *testing.T) {
	a, err := app.New(app.Options{StoreDir: t.TempDir(), AllowUnauthed: true})
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Close()

	group := types.NewJID("120363001234567890", types.GroupServer)
	sender := "15551234567@s.whatsapp.net"
	info, _, _, _, err := buildPollVoteInfo(context.Background(), a, group, "poll-id", sender)
	if err != nil {
		t.Fatalf("buildPollVoteInfo: %v", err)
	}
	if !info.IsGroup {
		t.Fatalf("IsGroup = false, want true")
	}
	if info.Chat != group {
		t.Fatalf("Chat = %s, want %s", info.Chat, group)
	}
	if info.Sender.String() != sender {
		t.Fatalf("Sender = %s, want %s", info.Sender, sender)
	}
}

func TestBuildPollVoteInfoFindsAlternateChatKey(t *testing.T) {
	a, err := app.New(app.Options{StoreDir: t.TempDir(), AllowUnauthed: true})
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Close()

	pnChat := types.NewJID("15551234567", types.DefaultUserServer)
	lidChat := types.NewJID("123456789", types.HiddenUserServer)
	sender := "15557654321@s.whatsapp.net"
	if err := a.DB().UpsertPoll(store.Poll{
		ChatJID:         lidChat.String(),
		MsgID:           "poll-id",
		SenderJID:       sender,
		Question:        "Dinner?",
		Options:         []string{"Yes", "No"},
		SelectableCount: 1,
		CreatedAt:       time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("UpsertPoll: %v", err)
	}

	info, options, selectable, matchedChatJID, err := buildPollVoteInfoForChats(
		context.Background(),
		a,
		pnChat,
		[]string{pnChat.String(), lidChat.String()},
		"poll-id",
		"",
	)
	if err != nil {
		t.Fatalf("buildPollVoteInfoForChats: %v", err)
	}
	if matchedChatJID != lidChat.String() {
		t.Fatalf("matchedChatJID = %q, want %q", matchedChatJID, lidChat.String())
	}
	if !reflect.DeepEqual(options, []string{"Yes", "No"}) {
		t.Fatalf("options = %v", options)
	}
	if selectable != 1 {
		t.Fatalf("selectable = %d, want 1", selectable)
	}
	if info.Sender.String() != sender {
		t.Fatalf("Sender = %s, want %s", info.Sender, sender)
	}
}

func TestGetPollForShowFindsNonADChat(t *testing.T) {
	a, err := app.New(app.Options{StoreDir: t.TempDir(), AllowUnauthed: true})
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Close()

	chat := types.NewJID("15551234567", types.DefaultUserServer)
	deviceChat := chat
	deviceChat.Device = 2
	if err := a.DB().UpsertPoll(store.Poll{
		ChatJID:         chat.String(),
		MsgID:           "poll-id",
		Question:        "Dinner?",
		Options:         []string{"Yes", "No"},
		SelectableCount: 1,
		CreatedAt:       time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("UpsertPoll: %v", err)
	}
	if err := a.DB().UpsertPollVote(store.PollVote{
		ChatJID:   chat.String(),
		PollMsgID: "poll-id",
		VoterJID:  "15557654321@s.whatsapp.net",
		VoteMsgID: "vote-id",
		Selected:  []string{"Yes"},
		VotedAt:   time.Date(2026, 5, 9, 12, 1, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("UpsertPollVote: %v", err)
	}

	poll, votes, err := getPollForShow(context.Background(), a, deviceChat, "poll-id")
	if err != nil {
		t.Fatalf("getPollForShow: %v", err)
	}
	if poll.ChatJID != chat.String() {
		t.Fatalf("ChatJID = %q, want %q", poll.ChatJID, chat.String())
	}
	if len(votes) != 1 || votes[0].VoteMsgID != "vote-id" {
		t.Fatalf("votes = %+v", votes)
	}
}

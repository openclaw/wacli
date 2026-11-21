package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/openclaw/wacli/internal/lock"
	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow/types"
)

// groupOpKind is the delegate kind for group administration. A running
// `wacli sync --follow` owns the store lock and the WhatsApp connection, so a
// group command first asks it over the control socket and only opens the store
// itself when no sync answers there.
const groupOpKind = "group"

const (
	groupActionCreate       = "create"
	groupActionInfo         = "info"
	groupActionRefresh      = "refresh"
	groupActionRename       = "rename"
	groupActionTopic        = "topic"
	groupActionParticipants = "participants"
	groupActionInviteLink   = "invite_link"
	groupActionPicture      = "picture"
	groupActionLink         = "link"
	groupActionUnlink       = "unlink"
)

// groupOp is one group operation, the same whether it runs in this process or
// in the sync daemon.
type groupOp struct {
	Action            string   `json:"action"`
	JID               string   `json:"jid,omitempty"`
	Parent            string   `json:"parent,omitempty"`
	Hidden            bool     `json:"hidden,omitempty"`
	Name              string   `json:"name,omitempty"`
	Text              string   `json:"text,omitempty"`
	Users             []string `json:"users,omitempty"`
	ParticipantAction string   `json:"participant_action,omitempty"`
	// Image carries JPEG bytes for set-picture (base64 on the wire). Empty with
	// Action=picture removes the picture.
	Image        []byte `json:"image,omitempty"`
	ResetInvite  bool   `json:"reset_invite,omitempty"`
	AnnounceOnly bool   `json:"announce_only,omitempty"`
	Locked       bool   `json:"locked,omitempty"`
	JoinApproval bool   `json:"join_approval,omitempty"`
	Community    bool   `json:"community,omitempty"`
	LinkedParent string `json:"linked_parent,omitempty"`
}

// groupOpResult is what the command prints: JSON is the --json payload and
// Text the human-readable output, so both paths print the same thing.
type groupOpResult struct {
	JSON json.RawMessage `json:"json,omitempty"`
	Text string          `json:"text,omitempty"`
}

// groupOpClient is the slice of the WhatsApp client group operations need.
type groupOpClient interface {
	ResolveLIDToPN(ctx context.Context, jid types.JID) types.JID
	GetJoinedGroups(ctx context.Context) ([]*types.GroupInfo, error)
	GetGroupInfo(ctx context.Context, jid types.JID) (*types.GroupInfo, error)
	CreateGroup(ctx context.Context, req wa.CreateGroupRequest) (*types.GroupInfo, error)
	SetGroupName(ctx context.Context, jid types.JID, name string) error
	SetGroupTopic(ctx context.Context, jid types.JID, topic string) error
	UpdateGroupParticipants(ctx context.Context, group types.JID, users []types.JID, action wa.GroupParticipantAction) ([]types.GroupParticipant, error)
	GetGroupInviteLink(ctx context.Context, group types.JID, reset bool) (string, error)
	SetGroupPhoto(ctx context.Context, jid types.JID, avatar []byte) (string, error)
	LinkGroup(ctx context.Context, parent, child types.JID, hidden bool) error
	GroupHidden(ctx context.Context, jid types.JID) (bool, error)
	UnlinkGroup(ctx context.Context, parent, child types.JID) error
}

// groupOpStore is where group snapshots land after an operation.
type groupOpStore interface {
	PersistGroup(ctx context.Context, info *types.GroupInfo) error
	ReplaceJoinedGroups(ctx context.Context, groups []*types.GroupInfo, now time.Time) error
}

type dbGroupStore struct {
	db       *store.DB
	resolver groupJIDResolver
}

func (s dbGroupStore) PersistGroup(ctx context.Context, info *types.GroupInfo) error {
	return persistGroupInfo(ctx, s.db, s.resolver, info)
}

func (s dbGroupStore) ReplaceJoinedGroups(ctx context.Context, groups []*types.GroupInfo, now time.Time) error {
	joined := map[string]bool{}
	for _, g := range groups {
		if g == nil {
			continue
		}
		joined[g.JID.String()] = true
		_ = persistGroupInfo(ctx, s.db, s.resolver, g)
		_ = s.db.UpsertChatMetadata(g.JID.String(), "group", g.GroupName.Name)
	}
	return s.db.MarkGroupsMissingFrom(joined, now)
}

func groupResult(body any, text string) (groupOpResult, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return groupOpResult{}, err
	}
	return groupOpResult{JSON: raw, Text: text}, nil
}

// refreshGroupSnapshot re-reads a group after a change so the local DB matches
// WhatsApp. Failures are ignored as before: the change itself already happened.
func refreshGroupSnapshot(ctx context.Context, client groupOpClient, st groupOpStore, jid types.JID) {
	if info, err := client.GetGroupInfo(ctx, jid); err == nil && info != nil {
		_ = st.PersistGroup(ctx, info)
	}
}

// groupInfoJSON is GroupInfo plus the community visibility WhatsApp reports
// for linked subgroups (whatsmeow does not parse it).
type groupInfoJSON struct {
	*types.GroupInfo
	Hidden      *bool  `json:"Hidden,omitempty"`
	HiddenError string `json:"HiddenError,omitempty"`
}

// runGroupOp executes one group operation against a connected client.
func runGroupOp(ctx context.Context, client groupOpClient, st groupOpStore, op groupOp) (groupOpResult, error) {
	switch op.Action {
	case groupActionCreate:
		if strings.TrimSpace(op.Name) == "" {
			return groupOpResult{}, fmt.Errorf("--name is required")
		}
		if op.Community && strings.TrimSpace(op.LinkedParent) != "" {
			return groupOpResult{}, fmt.Errorf("--community and --linked-parent cannot be combined")
		}
		participants, err := parseGroupUserJIDs(op.Users)
		if err != nil {
			return groupOpResult{}, err
		}
		var parentJID types.JID
		if strings.TrimSpace(op.LinkedParent) != "" {
			parentJID, err = parseGroupJID(op.LinkedParent)
			if err != nil {
				return groupOpResult{}, fmt.Errorf("parse --linked-parent: %w", err)
			}
		}
		info, err := client.CreateGroup(ctx, wa.CreateGroupRequest{
			Name:                   op.Name,
			Participants:           participants,
			IsAnnounce:             op.AnnounceOnly,
			IsLocked:               op.Locked,
			IsJoinApprovalRequired: op.JoinApproval,
			IsParent:               op.Community,
			LinkedParentJID:        parentJID,
		})
		if err != nil {
			return groupOpResult{}, err
		}
		if info == nil {
			return groupResult(nil, "OK\n")
		}
		_ = st.PersistGroup(ctx, info)
		return groupResult(info, fmt.Sprintf("JID: %s\nName: %s\n", info.JID.String(), sanitize(info.GroupName.Name)))

	case groupActionInfo:
		gjid, err := types.ParseJID(strings.TrimSpace(op.JID))
		if err != nil {
			return groupOpResult{}, err
		}
		info, err := client.GetGroupInfo(ctx, gjid)
		if err != nil {
			return groupOpResult{}, err
		}
		if info == nil {
			return groupOpResult{}, fmt.Errorf("group info not found for %s", gjid.String())
		}
		_ = st.PersistGroup(ctx, info)
		body := groupInfoJSON{GroupInfo: info}
		var text strings.Builder
		fmt.Fprintf(&text, "JID: %s\nName: %s\nOwner: %s\nType: %s\n",
			info.JID.String(),
			sanitize(info.GroupName.Name),
			info.OwnerJID.String(),
			groupKindLabel(info.IsParent, info.LinkedParentJID.String()),
		)
		if !info.LinkedParentJID.IsEmpty() {
			fmt.Fprintf(&text, "Parent: %s\n", info.LinkedParentJID.String())
			hidden, err := client.GroupHidden(ctx, gjid)
			if err == nil {
				body.Hidden = &hidden
				fmt.Fprintf(&text, "Hidden: %t\n", hidden)
			} else {
				body.HiddenError = err.Error()
			}
		}
		fmt.Fprintf(&text, "Created: %s\nParticipants: %d\n",
			info.GroupCreated.Local().Format(time.RFC3339),
			len(info.Participants),
		)
		return groupResult(body, text.String())

	case groupActionRefresh:
		gs, err := client.GetJoinedGroups(ctx)
		if err != nil {
			return groupOpResult{}, err
		}
		if err := st.ReplaceJoinedGroups(ctx, gs, time.Now().UTC()); err != nil {
			return groupOpResult{}, err
		}
		return groupResult(map[string]any{"groups": len(gs)}, fmt.Sprintf("Imported %d groups.\n", len(gs)))

	case groupActionRename:
		if strings.TrimSpace(op.JID) == "" || strings.TrimSpace(op.Name) == "" {
			return groupOpResult{}, fmt.Errorf("--jid and --name are required")
		}
		gjid, err := types.ParseJID(strings.TrimSpace(op.JID))
		if err != nil {
			return groupOpResult{}, err
		}
		if err := client.SetGroupName(ctx, gjid, op.Name); err != nil {
			return groupOpResult{}, err
		}
		refreshGroupSnapshot(ctx, client, st, gjid)
		return groupResult(map[string]any{"jid": gjid.String(), "name": op.Name}, "OK\n")

	case groupActionTopic:
		gjid, err := parseGroupJID(op.JID)
		if err != nil {
			return groupOpResult{}, err
		}
		if err := client.SetGroupTopic(ctx, gjid, op.Text); err != nil {
			return groupOpResult{}, err
		}
		refreshGroupSnapshot(ctx, client, st, gjid)
		return groupResult(map[string]any{"jid": gjid.String(), "topic": op.Text}, "OK\n")

	case groupActionParticipants:
		if len(op.Users) == 0 {
			return groupOpResult{}, fmt.Errorf("--jid and at least one --user are required")
		}
		switch wa.GroupParticipantAction(op.ParticipantAction) {
		case wa.GroupParticipantAdd, wa.GroupParticipantRemove, wa.GroupParticipantPromote, wa.GroupParticipantDemote:
		default:
			return groupOpResult{}, fmt.Errorf("unknown participant action: %q", op.ParticipantAction)
		}
		gjid, err := types.ParseJID(strings.TrimSpace(op.JID))
		if err != nil {
			return groupOpResult{}, err
		}
		jids, err := parseGroupUserJIDs(op.Users)
		if err != nil {
			return groupOpResult{}, err
		}
		updated, err := client.UpdateGroupParticipants(ctx, gjid, jids, wa.GroupParticipantAction(op.ParticipantAction))
		if err != nil {
			return groupOpResult{}, err
		}
		refreshGroupSnapshot(ctx, client, st, gjid)
		return groupResult(updated, "OK\n")

	case groupActionInviteLink:
		gjid, err := types.ParseJID(strings.TrimSpace(op.JID))
		if err != nil {
			return groupOpResult{}, err
		}
		link, err := client.GetGroupInviteLink(ctx, gjid, op.ResetInvite)
		if err != nil {
			return groupOpResult{}, err
		}
		body := map[string]any{"jid": gjid.String(), "link": link}
		if op.ResetInvite {
			body["revoked"] = true
		}
		return groupResult(body, link+"\n")

	case groupActionPicture:
		gjid, err := parseGroupJID(op.JID)
		if err != nil {
			return groupOpResult{}, err
		}
		var img []byte
		if len(op.Image) > 0 {
			img = op.Image
		}
		pictureID, err := client.SetGroupPhoto(ctx, gjid, img)
		if err != nil {
			return groupOpResult{}, fmt.Errorf("set group picture: %w", err)
		}
		text := fmt.Sprintf("Group picture updated (id: %s)\n", pictureID)
		if img == nil {
			text = "Group picture removed\n"
		}
		return groupResult(map[string]any{"jid": gjid.String(), "picture_id": pictureID}, text)

	case groupActionLink, groupActionUnlink:
		link := op.Action == groupActionLink
		if strings.TrimSpace(op.Parent) == "" {
			return groupOpResult{}, fmt.Errorf("--parent is required (community parent group JID)")
		}
		child, err := parseGroupJID(op.JID)
		if err != nil {
			return groupOpResult{}, err
		}
		parent, err := parseGroupJID(op.Parent)
		if err != nil {
			return groupOpResult{}, fmt.Errorf("--parent: %w", err)
		}
		if link {
			err = client.LinkGroup(ctx, parent, child, op.Hidden)
		} else {
			err = client.UnlinkGroup(ctx, parent, child)
		}
		if err != nil {
			return groupOpResult{}, fmt.Errorf("%s group: %w", op.Action, err)
		}
		refreshGroupSnapshot(ctx, client, st, child)
		body := map[string]any{"jid": child.String(), "parent": parent.String(), "linked": link}
		if link {
			body["hidden"] = op.Hidden
		}
		return groupResult(body, "OK\n")
	}
	return groupOpResult{}, fmt.Errorf("unsupported group action %q", op.Action)
}

// executeDelegatedGroup is the daemon side of a delegated group operation.
func executeDelegatedGroup(ctx context.Context, client groupOpClient, st groupOpStore, req sendDelegateRequest) (sendDelegateResponse, error) {
	if req.Group == nil {
		return sendDelegateResponse{}, fmt.Errorf("group request without an operation")
	}
	res, err := runGroupOp(ctx, client, st, *req.Group)
	if err != nil {
		return sendDelegateResponse{}, err
	}
	return sendDelegateResponse{OK: true, Group: &res}, nil
}

// explainGroupDelegateError names the cause when the sync process that owns
// the store cannot run group operations, instead of surfacing a raw rejection.
func explainGroupDelegateError(err error) error {
	if err == nil || !strings.Contains(err.Error(), "unsupported send kind") || !strings.Contains(err.Error(), groupOpKind) {
		return err
	}
	return fmt.Errorf("the running `wacli sync` holds the store lock but does not support group operations (it is an older or upstream build); nothing was changed. Run the sync with a wacli that has delegated groups, then run this again: %w", err)
}

// runGroupOpAnywhere runs op in the sync daemon when one is listening and in
// this process otherwise.
func runGroupOpAnywhere(ctx context.Context, flags *rootFlags, op groupOp) (groupOpResult, error) {
	resp, err := delegateSend(ctx, flags, sendDelegateRequest{Kind: groupOpKind, Group: &op})
	if err == nil {
		if resp.Group == nil {
			return groupOpResult{}, fmt.Errorf("the running sync answered the group %s without a result", op.Action)
		}
		return *resp.Group, nil
	}
	if !errors.Is(err, errSendDelegateUnavailable) {
		return groupOpResult{}, explainGroupDelegateError(err)
	}

	a, lk, err := newApp(ctx, flags, true, false)
	if err != nil {
		if lock.IsLocked(err) {
			return groupOpResult{}, fmt.Errorf("%w (no `wacli sync --follow` answered on the store's control socket, so this command needed the lock itself; the process above holds it)", err)
		}
		return groupOpResult{}, err
	}
	defer closeApp(a, lk)
	if err := a.EnsureAuthed(ctx); err != nil {
		return groupOpResult{}, err
	}
	if err := a.Connect(ctx, false, nil); err != nil {
		return groupOpResult{}, err
	}
	return runGroupOp(ctx, a.WA(), dbGroupStore{db: a.DB(), resolver: a.WA()}, op)
}

func writeGroupOpResult(flags *rootFlags, res groupOpResult) error {
	if flags.asJSON {
		raw := res.JSON
		if len(raw) == 0 {
			raw = json.RawMessage("null")
		}
		return out.WriteJSON(os.Stdout, raw)
	}
	_, err := fmt.Fprint(os.Stdout, res.Text)
	return err
}

// runGroupCommand is the CLI entry point shared by the group subcommands.
func runGroupCommand(flags *rootFlags, op groupOp, writes bool) error {
	if writes {
		if err := flags.requireWritable(); err != nil {
			return err
		}
	}
	ctx, cancel := withTimeout(context.Background(), flags)
	defer cancel()
	res, err := runGroupOpAnywhere(ctx, flags, op)
	if err != nil {
		return err
	}
	return writeGroupOpResult(flags, res)
}

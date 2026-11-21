package wa

import (
	"context"
	"fmt"

	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

type CreateGroupRequest struct {
	Name                   string
	Participants           []types.JID
	IsAnnounce             bool
	IsLocked               bool
	IsJoinApprovalRequired bool
	IsParent               bool
	LinkedParentJID        types.JID
}

func (c *Client) CreateGroup(ctx context.Context, req CreateGroupRequest) (*types.GroupInfo, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return nil, fmt.Errorf("not connected")
	}
	return cli.CreateGroup(ctx, whatsmeow.ReqCreateGroup{
		Name:         req.Name,
		Participants: req.Participants,
		GroupAnnounce: types.GroupAnnounce{
			IsAnnounce: req.IsAnnounce,
		},
		GroupLocked: types.GroupLocked{
			IsLocked: req.IsLocked,
		},
		GroupMembershipApprovalMode: types.GroupMembershipApprovalMode{
			IsJoinApprovalRequired: req.IsJoinApprovalRequired,
		},
		GroupParent: types.GroupParent{
			IsParent: req.IsParent,
		},
		GroupLinkedParent: types.GroupLinkedParent{
			LinkedParentJID: req.LinkedParentJID,
		},
	})
}

func (c *Client) GetJoinedGroups(ctx context.Context) ([]*types.GroupInfo, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return nil, fmt.Errorf("not connected")
	}
	return cli.GetJoinedGroups(ctx)
}

func (c *Client) SetGroupName(ctx context.Context, jid types.JID, name string) error {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return fmt.Errorf("not connected")
	}
	return cli.SetGroupName(ctx, jid, name)
}

func (c *Client) SetGroupTopic(ctx context.Context, jid types.JID, topic string) error {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return fmt.Errorf("not connected")
	}
	return cli.SetGroupTopic(ctx, jid, "", "", topic)
}

func (c *Client) SetGroupAnnounce(ctx context.Context, jid types.JID, announce bool) error {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return fmt.Errorf("not connected")
	}
	return cli.SetGroupAnnounce(ctx, jid, announce)
}

func (c *Client) SetGroupLocked(ctx context.Context, jid types.JID, locked bool) error {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return fmt.Errorf("not connected")
	}
	return cli.SetGroupLocked(ctx, jid, locked)
}

type GroupParticipantAction string

const (
	GroupParticipantAdd     GroupParticipantAction = "add"
	GroupParticipantRemove  GroupParticipantAction = "remove"
	GroupParticipantPromote GroupParticipantAction = "promote"
	GroupParticipantDemote  GroupParticipantAction = "demote"
)

func (c *Client) UpdateGroupParticipants(ctx context.Context, group types.JID, users []types.JID, action GroupParticipantAction) ([]types.GroupParticipant, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return nil, fmt.Errorf("not connected")
	}

	var a whatsmeow.ParticipantChange
	switch action {
	case GroupParticipantAdd:
		a = whatsmeow.ParticipantChangeAdd
	case GroupParticipantRemove:
		a = whatsmeow.ParticipantChangeRemove
	case GroupParticipantPromote:
		a = whatsmeow.ParticipantChangePromote
	case GroupParticipantDemote:
		a = whatsmeow.ParticipantChangeDemote
	default:
		return nil, fmt.Errorf("unknown participant action: %s", action)
	}

	return cli.UpdateGroupParticipants(ctx, group, users, a)
}

type GroupParticipantRequestAction string

const (
	GroupParticipantRequestApprove GroupParticipantRequestAction = "approve"
	GroupParticipantRequestReject  GroupParticipantRequestAction = "reject"
)

func (c *Client) GetGroupRequestParticipants(ctx context.Context, group types.JID) ([]types.GroupParticipantRequest, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return nil, fmt.Errorf("not connected")
	}
	return cli.GetGroupRequestParticipants(ctx, group)
}

func (c *Client) UpdateGroupRequestParticipants(ctx context.Context, group types.JID, users []types.JID, action GroupParticipantRequestAction) ([]types.GroupParticipant, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return nil, fmt.Errorf("not connected")
	}

	var a whatsmeow.ParticipantRequestChange
	switch action {
	case GroupParticipantRequestApprove:
		a = whatsmeow.ParticipantChangeApprove
	case GroupParticipantRequestReject:
		a = whatsmeow.ParticipantChangeReject
	default:
		return nil, fmt.Errorf("unknown participant request action: %s", action)
	}

	return cli.UpdateGroupRequestParticipants(ctx, group, users, a)
}

func (c *Client) GetGroupInviteLink(ctx context.Context, group types.JID, reset bool) (string, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return "", fmt.Errorf("not connected")
	}
	return cli.GetGroupInviteLink(ctx, group, reset)
}

func (c *Client) JoinGroupWithLink(ctx context.Context, code string) (types.JID, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return types.JID{}, fmt.Errorf("not connected")
	}
	return cli.JoinGroupWithLink(ctx, code)
}

func (c *Client) LeaveGroup(ctx context.Context, group types.JID) error {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return fmt.Errorf("not connected")
	}
	return cli.LeaveGroup(ctx, group)
}

// SetGroupPhoto sets (or, with nil avatar, removes) a group's picture. avatar must be JPEG bytes.
func (c *Client) SetGroupPhoto(ctx context.Context, jid types.JID, avatar []byte) (string, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return "", fmt.Errorf("not connected")
	}
	return cli.SetGroupPhoto(ctx, jid, avatar)
}

// LinkGroup adds an existing group to a community (parent group). With hidden, the group
// is left out of the community's group list (only its members see it). WhatsApp fixes the
// visibility when the group is linked; to change it, unlink and link again.
// whatsmeow's LinkGroup has no visibility option, so this sends the same w:g2 <links> IQ
// with a <hidden_group/> child on the group node, as WhatsApp Web does.
func (c *Client) LinkGroup(ctx context.Context, parent, child types.JID, hidden bool) error {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return fmt.Errorf("not connected")
	}
	if !hidden {
		return cli.LinkGroup(ctx, parent, child)
	}
	_, err := cli.DangerousInternals().SendIQ(ctx, whatsmeow.DangerousInfoQuery{
		Namespace: "w:g2",
		Type:      "set",
		To:        parent,
		Content: []waBinary.Node{{
			Tag: "links",
			Content: []waBinary.Node{{
				Tag:   "link",
				Attrs: waBinary.Attrs{"link_type": string(types.GroupLinkChangeTypeSub)},
				Content: []waBinary.Node{{
					Tag:     "group",
					Attrs:   waBinary.Attrs{"jid": child},
					Content: []waBinary.Node{{Tag: "hidden_group"}},
				}},
			}},
		}},
	})
	return err
}

// GroupHidden reports whether a community subgroup is hidden from the community's group
// list (<hidden_group/> in the live group info; whatsmeow does not parse it).
func (c *Client) GroupHidden(ctx context.Context, jid types.JID) (bool, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return false, fmt.Errorf("not connected")
	}
	res, err := cli.DangerousInternals().SendIQ(ctx, whatsmeow.DangerousInfoQuery{
		Namespace: "w:g2",
		Type:      "get",
		To:        jid,
		Content:   []waBinary.Node{{Tag: "query", Attrs: waBinary.Attrs{"request": "interactive"}}},
	})
	if err != nil {
		return false, err
	}
	group, ok := res.GetOptionalChildByTag("group")
	if !ok {
		return false, fmt.Errorf("no group node in response")
	}
	_, hidden := group.GetOptionalChildByTag("hidden_group")
	return hidden, nil
}

// UnlinkGroup removes a group from a community (parent group).
func (c *Client) UnlinkGroup(ctx context.Context, parent, child types.JID) error {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return fmt.Errorf("not connected")
	}
	return cli.UnlinkGroup(ctx, parent, child)
}

package wa

import (
	"context"
	"fmt"

	"go.mau.fi/whatsmeow"
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

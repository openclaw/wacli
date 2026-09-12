package wa

import (
	"context"
	"fmt"
	"strings"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

// resolvePNToLID uses the supplied session, falling back to the original JID
// when neither its stored mappings nor a user-info lookup resolves it.
func resolvePNToLID(ctx context.Context, cli *whatsmeow.Client, jid types.JID) types.JID {
	if cli == nil || cli.Store == nil {
		return jid
	}
	pn := jid.ToNonAD()
	if ownPN := cli.Store.GetJID().ToNonAD(); pn == ownPN {
		if ownLID := cli.Store.GetLID().ToNonAD(); !ownLID.IsEmpty() {
			return ownLID
		}
	}
	if cli.Store.LIDs == nil {
		return jid
	}
	lid, err := cli.Store.LIDs.GetLIDForPN(ctx, pn)
	if err == nil && !lid.IsEmpty() {
		return lid
	}
	info, err := cli.GetUserInfo(ctx, []types.JID{pn})
	if err == nil {
		if resolved := info[pn].LID.ToNonAD(); !resolved.IsEmpty() {
			return resolved
		}
	}
	return jid
}

func (c *Client) GetUserInfo(ctx context.Context, jids []types.JID) (map[types.JID]types.UserInfo, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return nil, fmt.Errorf("not connected")
	}
	return cli.GetUserInfo(ctx, jids)
}

func (c *Client) IsOnWhatsApp(ctx context.Context, phones []string) ([]types.IsOnWhatsAppResponse, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return nil, fmt.Errorf("not connected")
	}
	return cli.IsOnWhatsApp(ctx, phones)
}

func (c *Client) GetContact(ctx context.Context, jid types.JID) (types.ContactInfo, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || cli.Store == nil || cli.Store.Contacts == nil {
		return types.ContactInfo{}, fmt.Errorf("contacts store not available")
	}
	return cli.Store.Contacts.GetContact(ctx, jid)
}

func (c *Client) GetAllContacts(ctx context.Context) (map[types.JID]types.ContactInfo, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || cli.Store == nil || cli.Store.Contacts == nil {
		return nil, fmt.Errorf("contacts store not available")
	}
	return cli.Store.Contacts.GetAllContacts(ctx)
}

func (c *Client) ResolveLIDToPN(ctx context.Context, jid types.JID) types.JID {
	if jid.Server != types.HiddenUserServer {
		return jid
	}
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || cli.Store == nil || cli.Store.LIDs == nil {
		return jid
	}
	pn, err := cli.Store.LIDs.GetPNForLID(ctx, jid.ToNonAD())
	if err != nil || pn.IsEmpty() {
		return jid
	}
	return pn
}

func (c *Client) ResolvePNToLID(ctx context.Context, jid types.JID) types.JID {
	if jid.Server != types.DefaultUserServer {
		return jid
	}
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	return resolvePNToLID(ctx, cli, jid)
}

func BestContactName(info types.ContactInfo) string {
	if !info.Found {
		return ""
	}
	if s := strings.TrimSpace(info.FullName); s != "" {
		return s
	}
	if s := strings.TrimSpace(info.FirstName); s != "" {
		return s
	}
	if s := strings.TrimSpace(info.BusinessName); s != "" {
		return s
	}
	if s := strings.TrimSpace(info.PushName); s != "" && s != "-" {
		return s
	}
	if s := strings.TrimSpace(info.RedactedPhone); s != "" {
		return s
	}
	return ""
}

func (c *Client) ResolveChatName(ctx context.Context, chat types.JID, pushName string) string {
	fallback := chat.String()

	if chat.Server == types.NewsletterServer {
		meta, err := c.GetNewsletterInfo(ctx, chat)
		if err == nil && meta != nil {
			if name := NewsletterName(meta); name != "" {
				return name
			}
		}
	} else if chat.Server == types.GroupServer || chat.IsBroadcastList() {
		info, err := c.GetGroupInfo(ctx, chat)
		if err == nil && info != nil {
			if name := strings.TrimSpace(info.GroupName.Name); name != "" {
				return name
			}
		}
	} else {
		info, err := c.GetContact(ctx, chat.ToNonAD())
		if err == nil {
			if name := BestContactName(info); name != "" {
				return name
			}
		}
	}

	if name := strings.TrimSpace(pushName); name != "" && name != "-" {
		return name
	}
	return fallback
}

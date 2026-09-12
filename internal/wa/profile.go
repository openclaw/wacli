package wa

import (
	"context"
	"fmt"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

// SetProfilePicture sets the profile picture of the authenticated account.
// avatar must be JPEG bytes; pass nil to remove the picture.
// Returns the new picture ID assigned by WhatsApp.
//
// Uses DangerousInternals.SendIQ to send the w:profile:picture IQ stanza
// without a "target" attribute, which is the correct format for updating
// your own profile picture (as opposed to SetGroupPhoto which always sets target).
func (c *Client) SetProfilePicture(ctx context.Context, avatar []byte) (string, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return "", fmt.Errorf("not connected")
	}

	var content any
	if avatar != nil {
		content = []waBinary.Node{{
			Tag:     "picture",
			Attrs:   waBinary.Attrs{"type": "image"},
			Content: avatar,
		}}
	}

	resp, err := cli.DangerousInternals().SendIQ(ctx, whatsmeow.DangerousInfoQuery{
		Namespace: "w:profile:picture",
		Type:      "set",
		To:        types.ServerJID,
		Content:   content,
	})
	if err != nil {
		return "", err
	}
	if avatar == nil {
		return "remove", nil
	}
	pictureID, ok := resp.GetChildByTag("picture").Attrs["id"].(string)
	if !ok {
		return "", fmt.Errorf("no picture ID in response")
	}
	return pictureID, nil
}

func (c *Client) GetProfilePictureInfo(ctx context.Context, jid types.JID, preview bool, existingID string) (*types.ProfilePictureInfo, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return nil, fmt.Errorf("not connected")
	}
	return cli.GetProfilePictureInfo(ctx, jid, &whatsmeow.GetProfilePictureParams{
		Preview:    preview,
		ExistingID: existingID,
	})
}

func (c *Client) SetStatusMessage(ctx context.Context, msg string) error {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return fmt.Errorf("not connected")
	}
	return cli.SetStatusMessage(ctx, types.SetStatusInput{Text: &msg})
}

func (c *Client) SetProfileName(ctx context.Context, name string) error {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return fmt.Errorf("not connected")
	}
	if err := cli.SendAppState(ctx, appstate.BuildSettingPushName(name)); err != nil {
		return err
	}
	if cli.Store != nil {
		cli.Store.PushName = name
	}
	return nil
}

func (c *Client) GetBusinessProfile(ctx context.Context, jid types.JID) (*types.BusinessProfile, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return nil, fmt.Errorf("not connected")
	}
	return cli.GetBusinessProfile(ctx, jid)
}

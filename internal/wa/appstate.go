package wa

import (
	"context"
	"fmt"
	"time"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/types"
)

func (c *Client) SendAppState(ctx context.Context, patch appstate.PatchInfo) error {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return fmt.Errorf("not connected")
	}
	return cli.SendAppState(ctx, patch)
}

// HasLocalAppState reports whether whatsmeow has any local state for the
// given app state collection (a non-zero synced version). Used to decide
// whether settling replay debt with an incremental fetch is safe.
func (c *Client) HasLocalAppState(ctx context.Context, name string) (bool, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || cli.Store == nil || cli.Store.AppState == nil {
		return false, fmt.Errorf("not connected")
	}
	version, _, err := cli.Store.AppState.GetAppStateVersion(ctx, name)
	if err != nil {
		return false, err
	}
	return version > 0, nil
}

func (c *Client) sendAppStateWithBoundary(ctx context.Context, patch appstate.PatchInfo, beforeApply func()) ([]any, error) {
	// Reserve wacli persistence before whatsmeow can advance app state. The
	// durable recovery intent remains after return because canonical sends
	// dispatch their resulting events asynchronously.
	beforeApply()
	return nil, c.SendAppState(ctx, patch)
}

func (c *Client) ArchiveChat(ctx context.Context, target types.JID, archive bool, lastMsgTS time.Time, lastMsgKey *waCommon.MessageKey, beforeApply func()) ([]any, error) {
	return c.sendAppStateWithBoundary(ctx, appstate.BuildArchive(target, archive, lastMsgTS, lastMsgKey), beforeApply)
}

func (c *Client) PinChat(ctx context.Context, target types.JID, pin bool, beforeApply func()) ([]any, error) {
	return c.sendAppStateWithBoundary(ctx, appstate.BuildPin(target, pin), beforeApply)
}

func (c *Client) MuteChat(ctx context.Context, target types.JID, mute bool, duration time.Duration, beforeApply func()) ([]any, error) {
	return c.sendAppStateWithBoundary(ctx, appstate.BuildMute(target, mute, duration), beforeApply)
}

func (c *Client) MarkChatAsRead(ctx context.Context, target types.JID, read bool, lastMsgTS time.Time, lastMsgKey *waCommon.MessageKey, beforeApply func()) ([]any, error) {
	return c.sendAppStateWithBoundary(ctx, appstate.BuildMarkChatAsRead(target, read, lastMsgTS, lastMsgKey), beforeApply)
}

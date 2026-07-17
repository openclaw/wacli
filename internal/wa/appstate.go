package wa

import (
	"context"
	"fmt"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	waBinary "go.mau.fi/whatsmeow/binary"
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

func (c *Client) sendAppStateEvents(ctx context.Context, patch appstate.PatchInfo, allowRetry bool) ([]interface{}, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return nil, fmt.Errorf("not connected")
	}

	version, hash, err := cli.Store.AppState.GetAppStateVersion(ctx, string(patch.Type))
	if err != nil {
		return nil, err
	}
	latestKeyID, err := cli.Store.AppStateKeys.GetLatestAppStateSyncKeyID(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get latest app state key ID: %w", err)
	}
	if latestKeyID == nil {
		return nil, fmt.Errorf("no app state keys found, creating app state keys is not yet supported")
	}

	state := appstate.HashState{Version: version, Hash: hash}
	processor := appstate.NewProcessor(cli.Store, cli.Log.Sub("AppState"))
	encodedPatch, err := processor.EncodePatch(ctx, latestKeyID, state, patch)
	if err != nil {
		return nil, err
	}
	resp, err := cli.DangerousInternals().SendIQ(ctx, whatsmeow.DangerousInfoQuery{
		Namespace: "w:sync:app:state",
		Type:      "set",
		To:        types.ServerJID,
		Content: []waBinary.Node{{
			Tag: "sync",
			Content: []waBinary.Node{{
				Tag: "collection",
				Attrs: waBinary.Attrs{
					"name":            string(patch.Type),
					"version":         version,
					"return_snapshot": false,
				},
				Content: []waBinary.Node{{Tag: "patch", Content: encodedPatch}},
			}},
		}},
	})
	if err != nil {
		return nil, err
	}

	respCollection, ok := resp.GetOptionalChildByTag("sync", "collection")
	if !ok {
		return nil, &whatsmeow.ElementMissingError{Tag: "collection", In: "app state send response"}
	}
	if respCollection.AttrGetter().OptionalString("type") == "error" {
		errorTag, hasError := respCollection.GetOptionalChildByTag("error")
		mainErr := fmt.Errorf("%w: %s", whatsmeow.ErrAppStateUpdate, &respCollection)
		if hasError {
			mainErr = fmt.Errorf("%w (%s): %s", whatsmeow.ErrAppStateUpdate, patch.Type, &errorTag)
		}
		if !hasError || errorTag.AttrGetter().Int("code") != 409 || !allowRetry {
			return nil, mainErr
		}
		patches, parseErr := appstate.ParsePatchList(ctx, &respCollection, cli.DangerousInternals().DownloadExternalAppStateBlob)
		if parseErr != nil {
			return nil, fmt.Errorf("%w (also, parsing patches in the response failed: %w)", mainErr, parseErr)
		}
		var conflictEvents []interface{}
		if _, applyErr := cli.DangerousInternals().ApplyAppStatePatches(ctx, patch.Type, state, patches, false, &conflictEvents); applyErr != nil {
			return conflictEvents, fmt.Errorf("%w (also, applying patches in the response failed: %w)", mainErr, applyErr)
		}
		retryEvents, retryErr := c.sendAppStateEvents(ctx, patch, false)
		return append(conflictEvents, retryEvents...), retryErr
	}

	eventsToPersist, err := cli.DangerousInternals().FetchAppState(ctx, patch.Type, false, false)
	if err != nil {
		return eventsToPersist, fmt.Errorf("failed to fetch app state after sending update: %w", err)
	}
	return eventsToPersist, nil
}

func (c *Client) ArchiveChat(ctx context.Context, target types.JID, archive bool, lastMsgTS time.Time, lastMsgKey *waCommon.MessageKey) ([]interface{}, error) {
	return c.sendAppStateEvents(ctx, appstate.BuildArchive(target, archive, lastMsgTS, lastMsgKey), true)
}

func (c *Client) PinChat(ctx context.Context, target types.JID, pin bool) ([]interface{}, error) {
	return c.sendAppStateEvents(ctx, appstate.BuildPin(target, pin), true)
}

func (c *Client) MuteChat(ctx context.Context, target types.JID, mute bool, duration time.Duration) ([]interface{}, error) {
	return c.sendAppStateEvents(ctx, appstate.BuildMute(target, mute, duration), true)
}

func (c *Client) MarkChatAsRead(ctx context.Context, target types.JID, read bool, lastMsgTS time.Time, lastMsgKey *waCommon.MessageKey) ([]interface{}, error) {
	return c.sendAppStateEvents(ctx, appstate.BuildMarkChatAsRead(target, read, lastMsgTS, lastMsgKey), true)
}

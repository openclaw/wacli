package wa

import (
	"context"
	"fmt"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

// The SDK's encode/apply/fetch primitives remain the protocol owner. This
// boundary lets the caller persist the fetched result while holding its write
// coordinator, rather than admitting send echoes from detached goroutines.
type regularAppStateSender interface {
	state(context.Context, appstate.WAPatchName) (appstate.HashState, error)
	encode(context.Context, appstate.HashState, appstate.PatchInfo) ([]byte, error)
	exchange(context.Context, appstate.WAPatchName, uint64, []byte) (*waBinary.Node, error)
	conflicts(context.Context, appstate.HashState, appstate.WAPatchName, *waBinary.Node) ([]any, error)
	fetch(context.Context, appstate.WAPatchName) ([]any, error)
}

func sendRegularAppState(ctx context.Context, sdk regularAppStateSender, patch appstate.PatchInfo) ([]any, error) {
	var applied []any
	// Same envelope and single 409 retry as pinned SendAppState, but never
	// call its detached dispatch path. SDK-owned encoding, conflict decoding,
	// MAC checks and fetches return their events synchronously instead.
	for attempt := 0; ; attempt++ {
		state, err := sdk.state(ctx, patch.Type)
		if err != nil {
			return applied, err
		}
		encoded, err := sdk.encode(ctx, state, patch)
		if err != nil {
			return applied, err
		}
		response, err := sdk.exchange(ctx, patch.Type, state.Version, encoded)
		if err != nil {
			return applied, err
		}
		collection, ok := response.GetOptionalChildByTag("sync", "collection")
		if !ok {
			return applied, &whatsmeow.ElementMissingError{Tag: "collection", In: "app state send response"}
		}
		if collection.AttrGetter().OptionalString("type") == "error" {
			failure := fmt.Errorf("%w: %s", whatsmeow.ErrAppStateUpdate, &collection)
			errNode, found := collection.GetOptionalChildByTag("error")
			if !found || errNode.AttrGetter().Int("code") != 409 || attempt != 0 {
				return applied, failure
			}
			batch, err := sdk.conflicts(ctx, state, patch.Type, &collection)
			applied = append(applied, batch...)
			if err != nil {
				return applied, fmt.Errorf("%w (applying conflicting patches: %w)", failure, err)
			}
			continue
		}
		batch, err := sdk.fetch(ctx, patch.Type)
		applied = append(applied, batch...)
		if err != nil {
			return applied, fmt.Errorf("failed to fetch app state after sending update: %w", err)
		}
		return applied, nil
	}
}

type sdkRegularAppStateSender struct{ cli *whatsmeow.Client }

func (s sdkRegularAppStateSender) state(ctx context.Context, name appstate.WAPatchName) (appstate.HashState, error) {
	v, hash, err := s.cli.Store.AppState.GetAppStateVersion(ctx, string(name))
	return appstate.HashState{Version: v, Hash: hash}, err
}

func (s sdkRegularAppStateSender) encode(ctx context.Context, state appstate.HashState, patch appstate.PatchInfo) ([]byte, error) {
	id, err := s.cli.Store.AppStateKeys.GetLatestAppStateSyncKeyID(ctx)
	if err != nil {
		return nil, fmt.Errorf("get latest app state key: %w", err)
	}
	if id == nil {
		return nil, fmt.Errorf("no app state keys found, creating app state keys is not yet supported")
	}
	return appstate.NewProcessor(s.cli.Store, s.cli.Log.Sub("AppState")).EncodePatch(ctx, id, state, patch)
}

func (s sdkRegularAppStateSender) exchange(ctx context.Context, name appstate.WAPatchName, version uint64, patch []byte) (*waBinary.Node, error) {
	return s.cli.DangerousInternals().SendIQ(ctx, whatsmeow.DangerousInfoQuery{
		Namespace: "w:sync:app:state", Type: "set", To: types.ServerJID,
		Content: []waBinary.Node{{Tag: "sync", Content: []waBinary.Node{{Tag: "collection",
			Attrs:   waBinary.Attrs{"name": string(name), "version": version, "return_snapshot": false},
			Content: []waBinary.Node{{Tag: "patch", Content: patch}},
		}}}},
	})
}

func (s sdkRegularAppStateSender) conflicts(ctx context.Context, state appstate.HashState, name appstate.WAPatchName, collection *waBinary.Node) ([]any, error) {
	patches, err := appstate.ParsePatchList(ctx, collection, s.cli.DangerousInternals().DownloadExternalAppStateBlob)
	if err != nil {
		return nil, err
	}
	var events []any
	_, err = s.cli.DangerousInternals().ApplyAppStatePatches(ctx, name, state, patches, false, &events)
	return events, err
}

func (s sdkRegularAppStateSender) fetch(ctx context.Context, name appstate.WAPatchName) ([]any, error) {
	return s.cli.DangerousInternals().FetchAppState(ctx, name, false, false)
}

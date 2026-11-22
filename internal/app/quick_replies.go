package app

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// Current Business clients send 3 (two observed accounts; another sent 2).
// events.AppState omits the action version, so we cannot follow each account.
const quickReplyActionVersion = 3

// QuickReplyInput sets supplied fields and preserves unspecified ones.
type QuickReplyInput struct {
	Shortcut *string
	Message  *string
	Keywords *[]string
}

// Build only after the shared pre-write sync has refreshed the index. Regular
// sends return the SDK result batch synchronously under the reserved boundary.
func (a *App) writeRegularAppState(ctx context.Context, prepare func(time.Time) (appstate.PatchInfo, func() error, error)) error {
	release, err := a.beginChatStateWrite(ctx, appstate.WAPatchRegular)
	if err != nil {
		return err
	}
	defer release()
	at := nowUTC().Truncate(time.Millisecond)
	patch, persist, err := prepare(at)
	if err != nil {
		return err
	}
	patch.Timestamp = at
	pending, err := a.beginLocalAppStateWrite(appstate.WAPatchRegular)
	if err != nil {
		return err
	}
	batch, err := a.wa.SendAppStatePatch(ctx, patch, func() { pending.reserve(a) })
	if err != nil {
		return errors.Join(err, a.failLocalAppStateWrite(ctx, &pending, batch))
	}
	if !pending.reserved {
		return fmt.Errorf("WhatsApp app state send completed without an apply boundary")
	}
	return a.completeLocalAppStateWrite(ctx, &pending, batch, persist)
}

// CreateQuickReply uses a Unix-seconds ID, as Baileys does, skipping known IDs.
// An unseen reply created in the same second can still collide.
func (a *App) CreateQuickReply(ctx context.Context, in QuickReplyInput) (store.QuickReply, error) {
	if in.Shortcut == nil || in.Message == nil {
		return store.QuickReply{}, fmt.Errorf("quick reply shortcut and message are required")
	}
	if err := validateQuickReplyInput(in); err != nil {
		return store.QuickReply{}, err
	}
	var created store.QuickReply
	err := a.writeRegularAppState(ctx, func(at time.Time) (appstate.PatchInfo, func() error, error) {
		if _, err := a.db.ResolveQuickReply(*in.Shortcut); err == nil {
			return appstate.PatchInfo{}, nil, fmt.Errorf("quick reply %q already exists; use `quick-replies edit`", *in.Shortcut)
		} else if !errors.Is(err, store.ErrQuickReplyNotFound) {
			return appstate.PatchInfo{}, nil, err
		}
		id := at.Unix()
		for {
			_, found, err := a.db.QuickReplyByID(strconv.FormatInt(id, 10))
			if err != nil {
				return appstate.PatchInfo{}, nil, err
			}
			if !found {
				break
			}
			id++
		}
		q := store.QuickReply{ID: strconv.FormatInt(id, 10), Shortcut: *in.Shortcut, Message: *in.Message, Count: proto.Int32(0)}
		if in.Keywords != nil {
			q.Keywords = *in.Keywords
		}
		created = q
		return buildQuickReply(q), func() error { return a.db.UpsertQuickReply(q, at) }, nil
	})
	return normalizeQuickReply(created), err
}

func (a *App) EditQuickReply(ctx context.Context, ref string, in QuickReplyInput) (store.QuickReply, error) {
	if in.Shortcut == nil && in.Message == nil && in.Keywords == nil {
		return store.QuickReply{}, fmt.Errorf("nothing to change; pass a new shortcut, message or keywords")
	}
	if err := validateQuickReplyInput(in); err != nil {
		return store.QuickReply{}, err
	}
	return a.rewriteQuickReply(ctx, ref, func(q *store.QuickReply) {
		if in.Shortcut != nil {
			q.Shortcut = *in.Shortcut
		}
		if in.Message != nil {
			q.Message = *in.Message
		}
		if in.Keywords != nil {
			q.Keywords = *in.Keywords
		}
	})
}

func (a *App) DeleteQuickReply(ctx context.Context, ref string) (store.QuickReply, error) {
	return a.rewriteQuickReply(ctx, ref, func(q *store.QuickReply) { q.Deleted = true })
}

func (a *App) rewriteQuickReply(ctx context.Context, ref string, change func(*store.QuickReply)) (store.QuickReply, error) {
	var result store.QuickReply
	err := a.writeRegularAppState(ctx, func(at time.Time) (appstate.PatchInfo, func() error, error) {
		q, err := a.db.ResolveQuickReply(ref)
		if err != nil {
			return appstate.PatchInfo{}, nil, err
		}
		oldShortcut := q.Shortcut
		change(&q)
		if !q.Deleted && q.Shortcut != oldShortcut {
			if other, err := a.db.ResolveQuickReply(q.Shortcut); err == nil && other.ID != q.ID {
				return appstate.PatchInfo{}, nil, fmt.Errorf("quick reply %q already exists (ID %s)", q.Shortcut, other.ID)
			} else if err != nil && !errors.Is(err, store.ErrQuickReplyNotFound) {
				return appstate.PatchInfo{}, nil, err
			}
		}
		result = q
		return buildQuickReply(q), func() error { return a.db.UpsertQuickReply(q, at) }, nil
	})
	return normalizeQuickReply(result), err
}

func validateQuickReplyInput(in QuickReplyInput) error {
	if in.Shortcut != nil {
		s := strings.TrimPrefix(*in.Shortcut, "/")
		if strings.TrimSpace(s) == "" || s != strings.TrimSpace(s) {
			return fmt.Errorf("quick reply shortcut must be nonempty without surrounding spaces")
		}
	}
	if in.Message != nil && strings.TrimSpace(*in.Message) == "" {
		return fmt.Errorf("quick reply message must not be empty")
	}
	if in.Keywords != nil {
		for _, k := range *in.Keywords {
			if strings.TrimSpace(k) == "" {
				return fmt.Errorf("quick reply keywords must not be empty")
			}
		}
	}
	return nil
}

func buildQuickReply(q store.QuickReply) appstate.PatchInfo {
	return appstate.PatchInfo{Type: appstate.WAPatchRegular, Mutations: []appstate.MutationInfo{{
		Index: []string{appstate.IndexQuickReply, q.ID}, Version: quickReplyActionVersion,
		Value: &waSyncAction.SyncActionValue{QuickReplyAction: &waSyncAction.QuickReplyAction{
			Shortcut: proto.String(q.Shortcut), Message: proto.String(q.Message), Keywords: q.Keywords,
			Count: q.Count, Deleted: proto.Bool(q.Deleted), AssociatedLabelIDs: q.LabelIDs,
		}},
	}}}
}

func normalizeQuickReply(q store.QuickReply) store.QuickReply {
	if q.Keywords == nil {
		q.Keywords = []string{}
	}
	if q.LabelIDs == nil {
		q.LabelIDs = []string{}
	}
	return q
}

func (a *App) handleQuickReplyEvent(evt *events.AppState) error {
	if !isQuickReplyEvent(evt) {
		return nil
	}
	act := evt.GetQuickReplyAction()
	err := a.db.UpsertQuickReply(store.QuickReply{ID: evt.Index[1], Shortcut: act.GetShortcut(), Message: act.GetMessage(),
		Keywords: act.GetKeywords(), LabelIDs: act.GetAssociatedLabelIDs(), Count: act.Count, Deleted: act.GetDeleted(),
	}, time.UnixMilli(evt.GetTimestamp()))
	if err != nil {
		a.emitWarning("quick_reply_store_failed", fmt.Sprintf("warning: failed to store WhatsApp quick reply event: %v", err), nil)
	}
	return err
}

func isQuickReplyEvent(evt *events.AppState) bool {
	return evt != nil && evt.SyncActionValue != nil && evt.GetQuickReplyAction() != nil && len(evt.Index) >= 2 && evt.Index[0] == appstate.IndexQuickReply && evt.Index[1] != ""
}

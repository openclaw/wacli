package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

const (
	// Current Business clients send 3 (two observed accounts; another sent 2).
	// events.AppState omits the action version, so we cannot follow each account.
	quickReplyActionVersion = 3
	maxLabelColor           = 19
)

// LabelInput carries the fields a label write sets. Nil keeps the stored value.
type LabelInput struct {
	ID    string
	Name  *string
	Color *int32
}

// QuickReplyInput carries the fields a quick reply write sets. Nil keeps the
// stored value.
type QuickReplyInput struct {
	Shortcut *string
	Message  *string
	Keywords *[]string
}

// writeRegularAppState runs one `regular` collection write with the same
// ordering guarantees as chat state: recover and persist the collection first,
// then build the patch from the now current index, send it, and persist the
// local result in order with any events the send fetched.
func (a *App) writeRegularAppState(ctx context.Context, prepare func(at time.Time) (appstate.PatchInfo, func() error, error)) error {
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
	postSendEvents, err := a.wa.SendAppStatePatch(ctx, patch, func() { pending.reserve(a) })
	if err != nil {
		return errors.Join(err, a.failLocalAppStateWrite(ctx, &pending, postSendEvents))
	}
	if !pending.reserved {
		return fmt.Errorf("WhatsApp app state send completed without an apply boundary")
	}
	return a.completeLocalAppStateWrite(ctx, &pending, postSendEvents, persist)
}

func validateLabelInput(in LabelInput) error {
	if in.Name != nil && strings.TrimSpace(*in.Name) == "" {
		return fmt.Errorf("label name must not be empty")
	}
	if in.Color != nil && (*in.Color < 0 || *in.Color > maxLabelColor) {
		return fmt.Errorf("label color must be between 0 and %d", maxLabelColor)
	}
	return nil
}

// CreateLabel adds a label. Without an explicit ID it takes the next numeric
// ID, which is only safe once a full sync has shown every existing label.
func (a *App) CreateLabel(ctx context.Context, in LabelInput) (store.List, error) {
	if in.Name == nil {
		return store.List{}, fmt.Errorf("label name is required")
	}
	if err := validateLabelInput(in); err != nil {
		return store.List{}, err
	}
	id := strings.TrimSpace(in.ID)
	if in.ID != id {
		return store.List{}, fmt.Errorf("label ID must not have surrounding spaces")
	}
	if id != "" {
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil || n < 0 || strconv.FormatInt(n, 10) != id {
			return store.List{}, fmt.Errorf("label ID must be a canonical nonnegative numeric ID")
		}
	}
	var created store.List
	err := a.writeRegularAppState(ctx, func(at time.Time) (appstate.PatchInfo, func() error, error) {
		index, err := a.db.Lists()
		if err != nil {
			return appstate.PatchInfo{}, nil, err
		}
		for _, l := range index.Lists {
			if l.Name == *in.Name {
				return appstate.PatchInfo{}, nil, fmt.Errorf("a label named %q already exists (ID %s); use `lists edit`", l.Name, l.ID)
			}
		}
		if id == "" {
			if !index.FullSyncEventsSeen {
				return appstate.PatchInfo{}, nil, fmt.Errorf("the local label index has not seen a full sync, so the next label ID could overwrite a label it never saw; pass --id with an unused ID")
			}
			maxID, err := a.db.MaxNumericListID()
			if err != nil {
				return appstate.PatchInfo{}, nil, err
			}
			id = strconv.FormatInt(maxID+1, 10)
		} else if existing, found, err := a.db.ListByID(id); err != nil {
			return appstate.PatchInfo{}, nil, err
		} else if found {
			return appstate.PatchInfo{}, nil, fmt.Errorf("label ID %s is already used by %q; use `lists edit`", id, existing.Name)
		}
		color := int32(0)
		if in.Color != nil {
			color = *in.Color
		}
		patch := appstate.BuildLabelEdit(id, *in.Name, color, false)
		action := patch.Mutations[0].Value.GetLabelEditAction()
		// Match the account's model: where Lists exist, user labels are CUSTOM.
		if slices.ContainsFunc(index.Lists, func(l store.List) bool {
			return l.Type != nil && *l.Type == int32(waSyncAction.LabelEditAction_CUSTOM)
		}) {
			action.Type = waSyncAction.LabelEditAction_CUSTOM.Enum()
			action.IsActive = proto.Bool(true)
		}
		created = labelFromAction(id, action)
		return patch, func() error { return a.db.UpsertList(labelUpdate(id, action, at)) }, nil
	})
	return created, err
}

// EditLabel renames or recolors a label. WhatsApp replaces the whole value,
// so every stored field is resent, not only the changed ones.
func (a *App) EditLabel(ctx context.Context, ref string, in LabelInput) (store.List, error) {
	if in.Name == nil && in.Color == nil {
		return store.List{}, fmt.Errorf("nothing to change; pass a new name or color")
	}
	if err := validateLabelInput(in); err != nil {
		return store.List{}, err
	}
	return a.rewriteLabel(ctx, ref, in, false)
}

// DeleteLabel sends a label definition tombstone. Phone-side association
// cleanup has not been verified.
func (a *App) DeleteLabel(ctx context.Context, ref string) (store.List, error) {
	return a.rewriteLabel(ctx, ref, LabelInput{}, true)
}

func (a *App) rewriteLabel(ctx context.Context, ref string, in LabelInput, deleted bool) (store.List, error) {
	var result store.List
	err := a.writeRegularAppState(ctx, func(at time.Time) (appstate.PatchInfo, func() error, error) {
		current, err := a.db.ResolveListForWrite(ref)
		if err != nil {
			return appstate.PatchInfo{}, nil, err
		}
		if current.IsImmutable != nil && *current.IsImmutable {
			return appstate.PatchInfo{}, nil, fmt.Errorf("list %q is immutable on WhatsApp", current.Name)
		}
		if !current.ChatMembershipsSupported {
			return appstate.PatchInfo{}, nil, fmt.Errorf("list %q has system type %d; only labels and custom lists can be changed", current.Name, *current.Type)
		}
		name, color := current.Name, int32(0)
		if current.Color != nil {
			color = *current.Color
		}
		if in.Name != nil {
			name = *in.Name
			index, err := a.db.Lists()
			if err != nil {
				return appstate.PatchInfo{}, nil, err
			}
			for _, other := range index.Lists {
				if other.ID != current.ID && other.Name == name {
					return appstate.PatchInfo{}, nil, fmt.Errorf("a label named %q already exists (ID %s)", name, other.ID)
				}
			}
		}
		if in.Color != nil {
			color = *in.Color
		}
		patch := appstate.BuildLabelEdit(current.ID, name, color, deleted)
		action := patch.Mutations[0].Value.GetLabelEditAction()
		if in.Color == nil {
			action.Color = current.Color
		}
		action.PredefinedID, action.OrderIndex = current.PredefinedID, current.OrderIndex
		action.IsActive, action.IsImmutable = current.IsActive, current.IsImmutable
		action.MuteEndTimeMS = current.MuteEndTimeMS
		if current.Type != nil {
			action.Type = waSyncAction.LabelEditAction_ListType(*current.Type).Enum()
		}
		result = labelFromAction(current.ID, action)
		return patch, func() error { return a.db.UpsertList(labelUpdate(current.ID, action, at)) }, nil
	})
	return result, err
}

// ChatLabelResult reports the definition and the exact patched chat identity.
type ChatLabelResult struct {
	Label store.List
	Chat  string
}

// SetChatLabel adds a chat to, or removes it from, a label.
func (a *App) SetChatLabel(ctx context.Context, chat types.JID, ref string, labeled bool) (ChatLabelResult, error) {
	var result ChatLabelResult
	err := a.writeRegularAppState(ctx, func(at time.Time) (appstate.PatchInfo, func() error, error) {
		label, err := a.db.ResolveListForWrite(ref)
		if err != nil {
			return appstate.PatchInfo{}, nil, err
		}
		if !label.ChatMembershipsSupported {
			return appstate.PatchInfo{}, nil, fmt.Errorf("list %q has system type %d and no explicit chat memberships", label.Name, *label.Type)
		}
		target := a.canonicalStoreJID(ctx, chat)
		if target.Server == types.HiddenUserServer {
			return appstate.PatchInfo{}, nil, fmt.Errorf("chat %s has no known phone number; labeling LID-only chats is not supported", target)
		}
		jid := canonicalJIDString(target)
		result = ChatLabelResult{Label: label, Chat: jid}
		patch := appstate.BuildLabelChat(target, label.ID, labeled)
		return patch, func() error {
			if labeled {
				if err := a.db.UpsertChatMetadata(jid, chatKind(target), ""); err != nil {
					return err
				}
			}
			return a.db.SetChatList(jid, label.ID, labeled, at, false)
		}, nil
	})
	return result, err
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

// EditQuickReply changes a quick reply, resending its stored fields.
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

// DeleteQuickReply deletes a quick reply.
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
	return appstate.PatchInfo{
		Type: appstate.WAPatchRegular,
		Mutations: []appstate.MutationInfo{{
			Index:   []string{appstate.IndexQuickReply, q.ID},
			Version: quickReplyActionVersion,
			Value: &waSyncAction.SyncActionValue{
				QuickReplyAction: &waSyncAction.QuickReplyAction{
					Shortcut: proto.String(q.Shortcut), Message: proto.String(q.Message),
					Keywords: q.Keywords, Count: q.Count, Deleted: proto.Bool(q.Deleted),
					AssociatedLabelIDs: q.LabelIDs,
				},
			},
		}},
	}
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

func labelFromAction(id string, act *waSyncAction.LabelEditAction) store.List {
	l := store.List{ID: id, Name: act.GetName(), Color: act.Color, PredefinedID: act.PredefinedID,
		OrderIndex: act.OrderIndex, IsActive: act.IsActive, IsImmutable: act.IsImmutable, MuteEndTimeMS: act.MuteEndTimeMS}
	if act.Type != nil {
		t := int32(*act.Type)
		l.Type = &t
	}
	l.ChatMembershipsSupported = l.Type == nil || *l.Type == 0 || *l.Type == 4 || *l.Type == 5
	return l
}

func labelUpdate(id string, act *waSyncAction.LabelEditAction, at time.Time) store.ListUpdate {
	l := labelFromAction(id, act)
	return store.ListUpdate{ID: id, Name: act.Name, Color: l.Color, PredefinedID: l.PredefinedID, Type: l.Type,
		OrderIndex: l.OrderIndex, IsActive: l.IsActive, IsImmutable: l.IsImmutable, MuteEndTimeMS: l.MuteEndTimeMS, Deleted: act.Deleted, Timestamp: at}
}

// handleQuickReplyEvent persists quick replies, which whatsmeow only
// dispatches as generic app state events.
func (a *App) handleQuickReplyEvent(evt *events.AppState) error {
	if !isQuickReplyEvent(evt) {
		return nil
	}
	act := evt.GetQuickReplyAction()
	at := time.UnixMilli(evt.GetTimestamp())
	err := a.db.UpsertQuickReply(store.QuickReply{
		ID: evt.Index[1], Shortcut: act.GetShortcut(), Message: act.GetMessage(),
		Keywords: act.GetKeywords(), LabelIDs: act.GetAssociatedLabelIDs(), Count: act.Count, Deleted: act.GetDeleted(),
	}, at)
	if err != nil {
		a.emitWarning("quick_reply_store_failed", fmt.Sprintf("warning: failed to store WhatsApp quick reply event: %v", err), nil)
	}
	return err
}

func isQuickReplyEvent(evt *events.AppState) bool {
	return evt != nil && evt.SyncActionValue != nil && evt.GetQuickReplyAction() != nil &&
		len(evt.Index) >= 2 && evt.Index[0] == appstate.IndexQuickReply && evt.Index[1] != ""
}

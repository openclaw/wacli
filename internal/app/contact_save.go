package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types"
)

// SavedContact reports the account contact that was saved.
type SavedContact struct {
	JID       types.JID
	LID       types.JID
	FullName  string
	FirstName string
	Phonebook bool
}

// SaveContact saves an account contact under the app-state write coordinator.
// The resolved LID is required. Names come only from the SDK's contact-store
// journal, so later phone renames win and one-shot callers persist before exit.
// Debt is settled only after coverage and a successful journal drain; partial
// decoding can advance the SDK cursor without applying the batch's names.
func (a *App) SaveContact(ctx context.Context, target types.JID, fullName, firstName string, phonebook bool) (SavedContact, error) {
	lid := a.wa.ResolvePNToLID(ctx, target)
	if lid.Server != types.HiddenUserServer || lid.User == "" {
		return SavedContact{}, fmt.Errorf("could not find the WhatsApp ID (LID) of %s; nothing was saved (is the number on WhatsApp?)", target.User)
	}
	lid = lid.ToNonAD()
	patch, err := wa.BuildSaveContact(target, lid, fullName, firstName, phonebook)
	if err != nil {
		return SavedContact{}, err
	}
	action := patch.Mutations[0].Value.GetContactAction()
	saved := SavedContact{JID: target, LID: lid, FullName: action.GetFullName(), FirstName: action.GetFirstName(), Phonebook: phonebook}

	collection := appstate.WAPatchCriticalUnblockLow
	release, err := a.beginChatStateWrite(ctx, collection)
	if err != nil {
		return SavedContact{}, err
	}
	defer release()
	pending, err := a.beginLocalAppStateWrite(collection)
	if err != nil {
		return SavedContact{}, err
	}
	journal := a.wa.ContactJournal()
	var start wa.JournalMark
	if journal != nil {
		start = journal.Mark(string(collection))
	}
	postSendEvents, sendErr := a.wa.SendAppStatePatch(ctx, patch, func() { pending.reserve(a) })
	// Every contact store and cursor write whatsmeow made for this send is
	// journaled by now, including when the send failed half-way.
	covered, echoed, through := true, true, uint64(0)
	if journal != nil {
		var end wa.JournalMark
		covered, echoed, end = journal.Coverage(string(collection), target, start)
		through = end.Seq
	}
	if covered {
		a.trackContactDebt(pending.generation, through)
	}
	if sendErr != nil {
		failErr := a.failLocalAppStateWrite(ctx, &pending, postSendEvents)
		// A separate ticket also covers failures before the send boundary.
		ticket := a.appStatePersist.reserve()
		persistCtx := context.WithoutCancel(ctx)
		result := make(chan error, 1)
		frontier := a.appStatePersist.complete(ticket, func() {
			_, err := a.drainContactJournal(persistCtx)
			result <- err
		})
		drainErr := a.appStatePersist.waitThrough(persistCtx, frontier)
		if drainErr == nil {
			drainErr = <-result
		}
		return SavedContact{}, errors.Join(fmt.Errorf("save contact: %w", sendErr), failErr, drainErr)
	}
	if !pending.reserved {
		return SavedContact{}, fmt.Errorf("WhatsApp app state send completed without an apply boundary")
	}
	// The ticket reserved before the send orders the post-send events the
	// client returns (none with the pinned whatsmeow, which dispatches them
	// itself); the drain's order comes from the journal, not from the ticket.
	persistCtx := context.WithoutCancel(ctx)
	var drainErr error
	err = a.completeLocalAppStateWrite(ctx, &pending, postSendEvents, func() error {
		_, drainErr = a.drainContactJournal(persistCtx)
		return nil
	})
	if err != nil {
		return SavedContact{}, fmt.Errorf("contact saved on WhatsApp, but updating the local index failed: %w", err)
	}
	switch {
	case !covered:
		a.emitWarning(
			"contact_save_coverage_unknown",
			fmt.Sprintf("warning: %s changed more than this save during it; keeping the recovery intent for the next sync or write", collection),
			map[string]any{"name": string(collection)},
		)
	case !echoed:
		a.emitWarning(
			"contact_save_echo_pending",
			"warning: WhatsApp accepted the contact, but whatsmeow has not fetched it back yet; the local index shows the name after the next sync",
			map[string]any{"name": string(collection)},
		)
	case a.contactDebtOutstanding(pending.generation):
		// The debt stays until a later drain or app-state recovery covers it.
		if drainErr == nil {
			drainErr = fmt.Errorf("the contact journal is not mirrored through the writes of the send")
		}
		a.warnAppStateRecovery(string(collection), fmt.Errorf("mirror the contact writes of the save: %w", drainErr))
	}
	return saved, nil
}

// contactDebt is the write-ahead intent of one contact save: settled once the
// journal is mirrored through the last write whatsmeow journaled for its send.
type contactDebt struct {
	generation int64
	through    uint64
}

func (a *App) trackContactDebt(generation int64, through uint64) {
	a.contactDrainMu.Lock()
	defer a.contactDrainMu.Unlock()
	a.contactDebts = append(a.contactDebts, contactDebt{generation: generation, through: through})
}

func (a *App) contactDebtOutstanding(generation int64) bool {
	a.contactDrainMu.Lock()
	defer a.contactDrainMu.Unlock()
	for _, debt := range a.contactDebts {
		if debt.generation == generation {
			return true
		}
	}
	return false
}

// settleContactDebtsLocked clears the intents of the saves whose writes are
// mirrored through covered: only their own generations, never older debt
// left by a process that died before mirroring (see
// degradedAppStateReplay). Requires contactDrainMu.
func (a *App) settleContactDebtsLocked(covered uint64) {
	kept := a.contactDebts[:0]
	for _, debt := range a.contactDebts {
		if debt.through > covered {
			kept = append(kept, debt)
			continue
		}
		if err := a.db.ClearAppStateRecoveryIntent(string(appstate.WAPatchCriticalUnblockLow), debt.generation); err != nil {
			a.warnAppStateRecovery(string(appstate.WAPatchCriticalUnblockLow), fmt.Errorf("clear the settled contact debt: %w", err))
			kept = append(kept, debt)
		}
	}
	clear(a.contactDebts[len(kept):])
	a.contactDebts = kept
}

// drainContactJournal projects names (including clears and failed SDK writes)
// under canonical JIDs in one transaction. Drains serialize through Ack so
// PN/LID aliases keep application order. Failure acknowledges nothing.
// Returns the mirrored sequence frontier and settles covered saves' own debt.
func (a *App) drainContactJournal(ctx context.Context) (covered uint64, err error) {
	journal := a.wa.ContactJournal()
	a.contactDrainMu.Lock()
	defer a.contactDrainMu.Unlock()
	if journal == nil {
		a.settleContactDebtsLocked(0)
		return 0, nil
	}
	writes, highWater := journal.Snapshot()
	if len(writes) > 0 {
		names := make([]store.ContactActionName, 0, len(writes))
		for _, write := range writes {
			if write.JID.IsEmpty() {
				continue
			}
			contact := a.canonicalStoreJID(ctx, write.JID)
			if contact.Server != types.DefaultUserServer && contact.Server != types.HiddenUserServer {
				continue
			}
			phone := ""
			if contact.Server == types.DefaultUserServer {
				phone = contact.User
			}
			names = append(names, store.ContactActionName{
				JID: contact.String(), Phone: phone,
				FullName: strings.TrimSpace(write.FullName), FirstName: strings.TrimSpace(write.FirstName),
			})
		}
		if err := a.db.SetContactActionNames(names); err != nil {
			a.emitWarning(
				"contact_store_failed",
				fmt.Sprintf("warning: failed to store %d contact names from the session: %v", len(names), err),
				map[string]any{"count": len(names), "error": err.Error()},
			)
			return writes[0].Seq - 1, fmt.Errorf("mirror %d contact writes: %w", len(names), err)
		}
		journal.Ack(writes[len(writes)-1].Seq)
	}
	a.settleContactDebtsLocked(highWater)
	return highWater, nil
}

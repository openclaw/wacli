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

// SaveContact saves or renames a contact in the WhatsApp account through the
// `critical_unblock_low` app-state collection and mirrors the name into
// wacli.db. target must be a phone-number JID; the contact's LID is resolved
// over the connection and required, because current clients key contacts by
// it and show only the number for a contact saved without it. An empty
// firstName is derived from the first word of fullName.
//
// The write goes through the same coordinator as the chat-state writes: it
// waits for other app-state writes and recovery, synchronizes the collection
// first, records durable recovery debt before the send, and reserves the
// local mirror's place in the persistence order before whatsmeow can advance
// the collection. whatsmeow writes every contact mutation of the batches it
// applies for the send (the 409 conflict patches, the post-send fetch) to its
// contact store before SendAppState returns, and those writes are journaled in
// that order (wa.ContactStoreJournal); the events are only dispatched later.
// The save projects the journal under its reserved ticket, so the batch
// reaches wacli.db even if a one-shot save exits before the events are
// dispatched, and settles its own debt only once that projection covered
// every write of the send. The events, when they arrive, drain the journal
// again and never apply their payload, so a batch dispatched late cannot
// regress the mirror.
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
	// The pre-write sync persisted its own events; mirror anything still
	// journaled before the optimistic write takes its place in the order.
	if _, err := a.drainContactJournalUnderTicket(ctx); err != nil {
		return SavedContact{}, fmt.Errorf("mirror pending contact writes before the save: %w", err)
	}
	pending, err := a.beginLocalAppStateWrite(collection)
	if err != nil {
		return SavedContact{}, err
	}
	postSendEvents, err := a.wa.SaveContact(ctx, target, lid, saved.FullName, saved.FirstName, phonebook, func() { pending.reserve(a) })
	if err != nil {
		return SavedContact{}, errors.Join(fmt.Errorf("save contact: %w", err), a.failLocalAppStateWrite(ctx, &pending, postSendEvents))
	}
	if !pending.reserved {
		return SavedContact{}, fmt.Errorf("WhatsApp app state send completed without an apply boundary")
	}
	// Every store write whatsmeow made for this send is journaled by now.
	var highWater uint64
	if journal := a.wa.ContactJournal(); journal != nil {
		highWater = journal.HighWater()
	}
	persistCtx := context.WithoutCancel(ctx)
	var covered uint64
	var drainErr error
	err = a.completeLocalAppStateWrite(ctx, &pending, postSendEvents, func() error {
		if err := a.db.SetContactActionName(target.String(), target.User, saved.FullName, saved.FirstName); err != nil {
			return err
		}
		covered, drainErr = a.drainContactJournal(persistCtx)
		return nil
	})
	if err != nil {
		return SavedContact{}, fmt.Errorf("contact saved on WhatsApp, but updating the local index failed: %w", err)
	}
	if drainErr != nil || covered < highWater {
		// The debt recorded before the send stays until the primary's
		// snapshot replays the collection; see degradedAppStateReplay.
		if drainErr == nil {
			drainErr = fmt.Errorf("journal drained to %d, the send's writes reach %d", covered, highWater)
		}
		a.warnAppStateRecovery(string(collection), fmt.Errorf("mirror the contact writes of the save: %w", drainErr))
		return saved, nil
	}
	// Only this save's own intent: older debt belongs to writes this process
	// never saw (a save that died between its send and its mirror).
	if err := a.db.ClearAppStateRecoveryIntent(string(collection), pending.generation); err != nil {
		a.warnAppStateRecovery(string(collection), fmt.Errorf("clear the settled contact debt: %w", err))
	}
	return saved, nil
}

// drainContactJournal projects the contact store writes whatsmeow journaled
// into wacli.db, in the order the SDK applied them, and acknowledges them.
// Each write is stored under the canonical contact (a mapped LID lands on
// its phone number; an unmapped LID stays under the LID), with the names the
// SDK stored: a nameless action clears, exactly as whatsmeow's PutContactName
// stores empty names. A write whose store update failed is applied too: the
// mutation was decoded and the cursor advanced, so its names are WhatsApp's
// state. All writes of one drain go in one transaction; on failure nothing is
// acknowledged and the next drain retries them.
//
// Must run inside a persistence task: drains are serialized by the ticket
// order, which with the journal's sequence order makes the last write to a
// contact across its indices the one wacli.db ends on. Returns the sequence
// number every journaled write up to which has been mirrored.
func (a *App) drainContactJournal(ctx context.Context) (covered uint64, err error) {
	journal := a.wa.ContactJournal()
	if journal == nil {
		return 0, nil
	}
	writes, highWater := journal.Snapshot()
	if len(writes) == 0 {
		return highWater, nil
	}
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
	return highWater, nil
}

// drainContactJournalUnderTicket runs drainContactJournal as its own
// persistence task, ordered with the contact events.
func (a *App) drainContactJournalUnderTicket(ctx context.Context) (uint64, error) {
	ticket := a.appStatePersist.reserve()
	persistCtx := context.WithoutCancel(ctx)
	type result struct {
		covered uint64
		err     error
	}
	done := make(chan result, 1)
	frontier := a.appStatePersist.complete(ticket, func() {
		covered, err := a.drainContactJournal(persistCtx)
		done <- result{covered, err}
	})
	if err := a.appStatePersist.waitThrough(persistCtx, frontier); err != nil {
		return 0, err
	}
	r := <-done
	return r.covered, r.err
}

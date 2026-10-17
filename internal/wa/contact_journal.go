package wa

import (
	"context"
	"sync"

	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

// ContactWrite is one name write whatsmeow made to its contact store, in the
// order the SDK applied it. Err is the store's error for that write; the
// mutation was decoded and the collection cursor advanced regardless, so the
// intended names are WhatsApp's state even when Err is set.
type ContactWrite struct {
	Seq       uint64
	JID       types.JID
	FirstName string
	FullName  string
	Err       error
}

// ContactStoreJournal decorates the device's store.ContactStore and records
// every name write the SDK makes, in application order. whatsmeow's
// dispatchAppState calls PutContactName synchronously for every contact SET
// mutation while a batch is applied (inside fetchAppState, inside the 409
// retry of sendAppState, and inside a recovery snapshot), before the batch's
// events are handed to a goroutine. The journal therefore orders writes to
// different indices of one contact (its phone number and its LID), which the
// SDK store keeps as independent rows, and it records clears (empty names)
// and writes whose store update failed. The app projects the journal into
// wacli.db in sequence order and acknowledges what it applied.
//
// The argument order follows the SDK's caller and the sqlstore
// implementation, (firstName, fullName), not the parameter names of the
// store.ContactStore interface declaration, which are swapped.
type ContactStoreJournal struct {
	store.ContactStore

	mu      sync.Mutex
	seq     uint64
	pending []ContactWrite
}

// NewContactStoreJournal wraps inner. Every other ContactStore method passes
// through untouched.
func NewContactStoreJournal(inner store.ContactStore) *ContactStoreJournal {
	return &ContactStoreJournal{ContactStore: inner}
}

func (j *ContactStoreJournal) PutContactName(ctx context.Context, user types.JID, firstName, fullName string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	err := j.ContactStore.PutContactName(ctx, user, firstName, fullName)
	j.record(user, firstName, fullName, err)
	return err
}

func (j *ContactStoreJournal) PutAllContactNames(ctx context.Context, contacts []store.ContactEntry) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	err := j.ContactStore.PutAllContactNames(ctx, contacts)
	for _, contact := range contacts {
		j.record(contact.JID, contact.FirstName, contact.FullName, err)
	}
	return err
}

func (j *ContactStoreJournal) record(user types.JID, firstName, fullName string, err error) {
	j.seq++
	j.pending = append(j.pending, ContactWrite{Seq: j.seq, JID: user, FirstName: firstName, FullName: fullName, Err: err})
}

// HighWater returns the sequence number of the latest recorded write.
func (j *ContactStoreJournal) HighWater() uint64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.seq
}

// Pending returns the writes not yet acknowledged, in sequence order.
func (j *ContactStoreJournal) Pending() []ContactWrite {
	pending, _ := j.Snapshot()
	return pending
}

// Snapshot returns the pending writes together with the high-water sequence
// at the same instant, so a caller that applies them all knows every write
// up to highWater is mirrored.
func (j *ContactStoreJournal) Snapshot() (pending []ContactWrite, highWater uint64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]ContactWrite(nil), j.pending...), j.seq
}

// Ack drops every pending write with a sequence number up to seq.
func (j *ContactStoreJournal) Ack(seq uint64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	kept := j.pending[:0]
	for _, write := range j.pending {
		if write.Seq > seq {
			kept = append(kept, write)
		}
	}
	for i := len(kept); i < len(j.pending); i++ {
		j.pending[i] = ContactWrite{}
	}
	j.pending = kept
}

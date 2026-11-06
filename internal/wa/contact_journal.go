package wa

import (
	"context"
	"sync"

	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

// ContactWrite is a decoded name write in SDK application order, including
// writes whose store update failed after the cursor had already advanced.
type ContactWrite struct {
	Seq       uint64
	JID       types.JID
	FirstName string
	FullName  string
}

// ContactStoreJournal records synchronous SDK name writes before asynchronous
// event delivery, preserving order across independent PN and LID rows. The app
// projects these writes (including clears and failures) and acknowledges them.
//
// The argument order follows the SDK's caller and the sqlstore
// implementation, (firstName, fullName), not the parameter names of the
// store.ContactStore interface declaration, which are swapped.
type ContactStoreJournal struct {
	store.ContactStore

	mu      sync.Mutex
	seq     uint64
	pending []ContactWrite
	last    map[types.JID]uint64
	// Cursor writes per app-state collection, in the same sequence as the
	// contact writes (see AppStateStore).
	cursorWrites map[string]uint64
	lastCursor   map[string]uint64
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
	j.record(user, firstName, fullName)
	return err
}

func (j *ContactStoreJournal) PutAllContactNames(ctx context.Context, contacts []store.ContactEntry) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	err := j.ContactStore.PutAllContactNames(ctx, contacts)
	for _, contact := range contacts {
		j.record(contact.JID, contact.FirstName, contact.FullName)
	}
	return err
}

func (j *ContactStoreJournal) record(user types.JID, firstName, fullName string) {
	j.seq++
	j.pending = append(j.pending, ContactWrite{Seq: j.seq, JID: user, FirstName: firstName, FullName: fullName})
	if j.last == nil {
		j.last = make(map[types.JID]uint64)
	}
	j.last[user.ToNonAD()] = j.seq
}

// NoteCursorWrite records that whatsmeow stored or deleted the version of the
// named app-state collection. DecodePatches stores it for every patch it
// decodes, before any name of the batch is applied, and a batch that fails on
// a later patch keeps the versions it stored without applying any name.
func (j *ContactStoreJournal) NoteCursorWrite(name string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.seq++
	if j.cursorWrites == nil {
		j.cursorWrites = make(map[string]uint64)
		j.lastCursor = make(map[string]uint64)
	}
	j.cursorWrites[name]++
	j.lastCursor[name] = j.seq
}

// JournalMark is a point in the journal: its sequence number and how many
// times the collection's cursor had been written by then.
type JournalMark struct {
	Seq          uint64
	CursorWrites uint64
}

// Mark returns the current point in the journal for the named collection.
func (j *ContactStoreJournal) Mark(name string) JournalMark {
	j.mu.Lock()
	defer j.mu.Unlock()
	return JournalMark{Seq: j.seq, CursorWrites: j.cursorWrites[name]}
}

// Coverage tells whether every patch of the named collection that whatsmeow
// consumed since start is known to have been applied: none was (the cursor
// was not written), or exactly one was and a name was written for own after
// that cursor write. whatsmeow writes a batch's names together, after it
// stored the versions of all its patches, so a name written after the only
// cursor write since start proves that patch's batch was applied. Anything
// else (a conflict retry, a concurrent edit, a batch that failed half-way)
// is not known to be covered. It also reports whether a name was written for
// own since start, and the point at which it decided.
func (j *ContactStoreJournal) Coverage(name string, own types.JID, start JournalMark) (covered, ownWritten bool, end JournalMark) {
	j.mu.Lock()
	defer j.mu.Unlock()
	end = JournalMark{Seq: j.seq, CursorWrites: j.cursorWrites[name]}
	last := j.last[own.ToNonAD()]
	ownWritten = last > start.Seq
	switch end.CursorWrites - start.CursorWrites {
	case 0:
		covered = true
	case 1:
		covered = last > j.lastCursor[name]
	}
	return covered, ownWritten, end
}

// AppStateStore wraps the device's app-state store so the journal records
// every cursor write (NoteCursorWrite) in order with the contact writes.
// Everything else passes through.
func (j *ContactStoreJournal) AppStateStore(inner store.AppStateStore) store.AppStateStore {
	return &journaledAppStateStore{AppStateStore: inner, journal: j}
}

type journaledAppStateStore struct {
	store.AppStateStore
	journal *ContactStoreJournal
}

func (s *journaledAppStateStore) PutAppStateVersion(ctx context.Context, name string, version uint64, hash [128]byte) error {
	err := s.AppStateStore.PutAppStateVersion(ctx, name, version, hash)
	if err == nil {
		s.journal.NoteCursorWrite(name)
	}
	return err
}

func (s *journaledAppStateStore) DeleteAppStateVersion(ctx context.Context, name string) error {
	err := s.AppStateStore.DeleteAppStateVersion(ctx, name)
	if err == nil {
		s.journal.NoteCursorWrite(name)
	}
	return err
}

// Snapshot returns the writes not yet acknowledged, in sequence order,
// together with the high-water sequence at the same instant, so a caller that
// applies them all knows every write up to highWater is mirrored.
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

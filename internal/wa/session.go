package wa

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	_ "github.com/mattn/go-sqlite3"
	"github.com/openclaw/wacli/internal/sqliteutil"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types/events"
)

func (c *Client) init() (err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	ctx := context.Background()
	dbLog := newWhatsmeowLogger("Database", "ERROR", os.Stderr)
	if err := sqliteutil.ChmodFiles(c.opts.StorePath, 0o600); err != nil {
		return err
	}
	container, err := sqlstore.New(ctx, "sqlite3", sqliteutil.FileURI(c.opts.StorePath, "_foreign_keys=on"), dbLog)
	if err != nil {
		return fmt.Errorf("open whatsmeow store: %w", err)
	}
	defer func() {
		if err != nil {
			_ = container.Close()
		}
	}()
	if err := sqliteutil.ChmodFiles(c.opts.StorePath, 0o600); err != nil {
		return err
	}

	deviceStore, err := container.GetFirstDevice(ctx)
	if err != nil {
		if err == sql.ErrNoRows {
			deviceStore = container.NewDevice()
		} else {
			return fmt.Errorf("get device store: %w", err)
		}
	}

	// Journal the contact store writes before the client exists, so every
	// name whatsmeow stores while applying critical_unblock_low is recorded
	// in application order (see ContactStoreJournal).
	c.contactJournal = journalContactStore(deviceStore)
	logger := newWhatsmeowLogger("Client", "ERROR", os.Stderr)
	c.client = whatsmeow.NewClient(deviceStore, logger)
	// FetchAppStateEvents fails closed without this: recovery snapshots must
	// return every mutation so wacli can rebuild its own database.
	c.client.EmitAppStateEventsOnFullSync = true
	// Persist recently-sent messages so whatsmeow can answer retry-receipts
	// across process restarts. Without this, recipients whose Signal session
	// has not been freshly bootstrapped (typically other linked devices) see
	// "Waiting for this message" indefinitely because whatsmeow can't find the
	// original plaintext to re-encrypt when the retry arrives.
	c.client.UseRetryMessageStore = true
	cli := c.client
	onEmptyKey := func(keyID []byte) {
		cli.DangerousInternals().DispatchEvent(&AppStateKeyUnavailable{KeyID: keyID})
	}
	guardAppStateKeys(deviceStore, c.opts.KeyStateStore, onEmptyKey)
	cli.AddEventHandler(func(evt any) {
		if _, ok := evt.(*events.PairSuccess); ok {
			guardAppStateKeys(cli.Store, c.opts.KeyStateStore, onEmptyKey)
			// Pairing's Device.Save replaced the stores. whatsmeow dispatches
			// PairSuccess right after it answered the pairing request; the
			// server then closes the stream, and app state is only fetched
			// after the reconnect and login that follow, so nothing writes
			// cli.Store.Contacts while it is rewrapped here.
			journal := journalContactStore(cli.Store)
			c.mu.Lock()
			c.contactJournal = journal
			c.mu.Unlock()
		}
	})
	// Let whatsmeow own bounded primary-device retries and cancellation for
	// eligible decryption failures.
	c.client.AutomaticMessageRerequestFromPhone = true
	applyDeliveryGuarantees(c.client)
	c.container = container
	return nil
}

// applyDeliveryGuarantees makes a refused event recoverable. The sync handler
// refuses (returns false) a message it could not store; whatsmeow then sends no
// receipt for it. Without the decrypted-event buffer a redelivery fails (the
// Signal counter already moved: ErrOldCounter) and the message is dropped; with
// it, whatsmeow replays the stored plaintext. The same buffer covers a crash
// between decrypt and store.
func applyDeliveryGuarantees(cli *whatsmeow.Client) {
	cli.EnableDecryptedEventBuffer = true
}

// journalContactStore wraps the device's contact store in a
// ContactStoreJournal, and its app-state store so the journal also records
// the collection cursor writes, once, and returns the journal.
func journalContactStore(device *store.Device) *ContactStoreJournal {
	if device == nil || device.Contacts == nil {
		return nil
	}
	if journal, ok := device.Contacts.(*ContactStoreJournal); ok {
		return journal
	}
	journal := NewContactStoreJournal(device.Contacts)
	device.Contacts = journal
	if device.AppState != nil {
		device.AppState = journal.AppStateStore(device.AppState)
	}
	return journal
}

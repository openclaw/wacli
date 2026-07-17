package wa

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	_ "github.com/mattn/go-sqlite3"
	"github.com/openclaw/wacli/internal/sqliteutil"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
)

func (c *Client) init() error {
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
	return nil
}

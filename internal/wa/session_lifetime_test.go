package wa

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/store/sqlstore"
)

func TestSessionDatabaseClosedWithClient(t *testing.T) {
	c, err := New(Options{StorePath: filepath.Join(t.TempDir(), "session.db")})
	if err != nil {
		t.Fatal(err)
	}
	container := c.client.Store.Container.(*sqlstore.Container)
	t.Cleanup(func() { _ = container.Close() })
	c.Close()
	if _, err := container.GetFirstDevice(context.Background()); err == nil || !strings.Contains(err.Error(), "database is closed") {
		t.Fatalf("session query after Client.Close = %v, want closed database", err)
	}
	c.Close()
}

func TestDisconnectRetainsSessionDatabase(t *testing.T) {
	c, err := New(Options{StorePath: filepath.Join(t.TempDir(), "session.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	container := c.client.Store.Container.(*sqlstore.Container)
	c.Disconnect()
	if _, err := container.GetFirstDevice(context.Background()); err != nil {
		t.Fatalf("session query after Disconnect: %v", err)
	}
	// An unpaired client must still reach normal authentication validation.
	if err := c.Connect(context.Background(), ConnectOptions{}); err == nil || !strings.Contains(err.Error(), "not authenticated") {
		t.Fatalf("Connect after Disconnect = %v, want pairing required", err)
	}
}

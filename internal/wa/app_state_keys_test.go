package wa

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/openclaw/wacli/internal/sqliteutil"
)

func newPairedSessionStore(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.db")
	ctx := context.Background()
	container, err := sqlstore.New(ctx, "sqlite3", sqliteutil.FileURI(path, "_foreign_keys=on"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer container.Close()
	device := container.NewDevice()
	device.ID = &types.JID{User: "15550000000", Device: 7, Server: types.DefaultUserServer}
	device.Account = testAccount()
	if err := device.Save(ctx); err != nil {
		t.Fatal(err)
	}
	return path
}

func testAccount() *waAdv.ADVSignedDeviceIdentity {
	return &waAdv.ADVSignedDeviceIdentity{
		Details:             []byte{1},
		AccountSignature:    make([]byte, 64),
		AccountSignatureKey: make([]byte, 32),
		DeviceSignature:     make([]byte, 64),
	}
}

func appStateKeyShare(id []byte, data []byte) *waE2E.AppStateSyncKey {
	keyData := &waE2E.AppStateSyncKeyData{
		Fingerprint: &waE2E.AppStateSyncKeyFingerprint{RawID: proto.Uint32(1), CurrentIndex: proto.Uint32(1)},
		Timestamp:   proto.Int64(1_790_000_000_000),
	}
	if data != nil {
		keyData.KeyData = data
	}
	return &waE2E.AppStateSyncKey{KeyID: &waE2E.AppStateSyncKeyId{KeyID: id}, KeyData: keyData}
}

// captureStderr swaps os.Stderr before the client is built, because the
// whatsmeow logger binds to the stream at construction time.
func captureStderr(t *testing.T) func() string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()
	var once sync.Once
	restore := func() string {
		once.Do(func() {
			os.Stderr = orig
			_ = w.Close()
			<-done
		})
		return buf.String()
	}
	t.Cleanup(func() { restore() })
	return restore
}

func TestAppStateKeyShareWithoutDataIsSkippedAndReported(t *testing.T) {
	path := newPairedSessionStore(t)
	stderr := captureStderr(t)
	c, err := New(Options{StorePath: path})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var mu sync.Mutex
	var unavailable [][]byte
	c.AddEventHandler(func(evt any) {
		if v, ok := evt.(*AppStateKeyUnavailable); ok {
			mu.Lock()
			unavailable = append(unavailable, v.KeyID)
			mu.Unlock()
		}
	})

	emptyID := []byte{0, 0, 0, 0, 0x7C, 0x57}
	goodID := []byte{0, 0, 0, 0, 0x7C, 0x58}
	goodData := bytes.Repeat([]byte{0xAB}, 32)
	ctx := context.Background()
	c.client.DangerousInternals().HandleAppStateSyncKeyShare(ctx, &waE2E.AppStateSyncKeyShare{
		Keys: []*waE2E.AppStateSyncKey{appStateKeyShare(emptyID, nil), appStateKeyShare(goodID, goodData)},
	})
	logs := stderr()

	if got, err := c.client.Store.AppStateKeys.GetAppStateSyncKey(ctx, emptyID); err != nil || got != nil {
		t.Fatalf("empty share stored as %+v (err %v), want no row", got, err)
	}
	got, err := c.client.Store.AppStateKeys.GetAppStateSyncKey(ctx, goodID)
	if err != nil || got == nil || !bytes.Equal(got.Data, goodData) {
		t.Fatalf("valid share = %+v (err %v), want stored 32-byte key", got, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(unavailable) != 1 || !bytes.Equal(unavailable[0], emptyID) {
		t.Fatalf("AppStateKeyUnavailable events = %X, want one for %X", unavailable, emptyID)
	}
	if strings.Contains(logs, "NOT NULL constraint failed") {
		t.Fatalf("empty share still reached SQL:\n%s", logs)
	}
	if want := "Failed to store app state sync key 000000007C57: " + ErrEmptyAppStateKeyShare.Error(); strings.Count(logs, want) != 1 {
		t.Fatalf("logs missing exactly one %q:\n%s", want, logs)
	}
}

func TestAppStateKeyGuardSurvivesPairing(t *testing.T) {
	c, err := New(Options{StorePath: filepath.Join(t.TempDir(), "session.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// Saving a new device swaps in fresh SQL stores, as whatsmeow does while pairing.
	c.client.Store.ID = &types.JID{User: "15550000000", Device: 7, Server: types.DefaultUserServer}
	c.client.Store.Account = testAccount()
	if err := c.client.Store.Save(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.client.Store.AppStateKeys.(*appStateKeyGuard); ok {
		t.Fatal("test premise: saving the device should replace the key store")
	}
	c.client.DangerousInternals().DispatchEvent(&events.PairSuccess{ID: *c.client.Store.ID})
	if _, ok := c.client.Store.AppStateKeys.(*appStateKeyGuard); !ok {
		t.Fatalf("key store after PairSuccess = %T, want guard", c.client.Store.AppStateKeys)
	}
}

package app

import (
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow/types"
)

func TestCallHistorySurvivesReconciliationCLI(t *testing.T) {
	a := newTestApp(t)
	h := newRetrievalCLI(t, a)
	dir := a.StoreDir()
	lid := types.JID{User: "999123456789", Server: types.HiddenUserServer}
	pn := types.JID{User: "15551234567", Server: types.DefaultUserServer}
	group := "120363000000@g.us"
	at := time.Unix(1700000000, 0).UTC()
	for _, jid := range []string{lid.String(), group} {
		if err := a.db.UpsertChat(jid, "unknown", "Synthetic", at); err != nil {
			t.Fatal(err)
		}
		if err := a.db.UpsertCallEvent(store.UpsertCallEventParams{
			ChatJID: jid, SenderJID: lid.String(), CallID: "synthetic-call", EventType: "offer", Timestamp: at,
		}); err != nil {
			t.Fatal(err)
		}
	}
	a.Close()
	before := h.run(dir, "", "calls", "list", "--asc")
	if rows, ok := before["calls"].([]any); !ok || len(rows) != 2 {
		t.Fatalf("before: %#v", before)
	}
	reopened, err := New(Options{StoreDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	a = reopened
	defer a.Close()
	f := newFakeWA()
	f.lids[lid] = pn
	a.wa = f
	for range 2 {
		if err := a.EnsureAuthed(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	a.Close()
	after := h.run(dir, "", "calls", "list", "--asc")
	rows, ok := after["calls"].([]any)
	if !ok || len(rows) != 2 {
		t.Fatalf("after: %#v", after)
	}
	for _, value := range rows {
		row := value.(map[string]any)
		if row["sender_jid"] != pn.String() || row["chat_jid"] == lid.String() || row["call_id"] != "synthetic-call" {
			t.Fatalf("unreconciled call: %#v", row)
		}
	}
	if oldBinary := os.Getenv("WACLI_COMPAT_CLI"); oldBinary != "" {
		old := &retrievalCLI{t: t, bin: oldBinary}
		if got := old.run(dir, "", "calls", "list", "--asc"); !reflect.DeepEqual(got, after) {
			t.Fatalf("previous binary sees different call history: %#v", got)
		}
	}
	filtered := h.run(dir, "", "calls", "list", "--chat", pn.String())
	if rows, ok := filtered["calls"].([]any); !ok || len(rows) != 1 {
		t.Fatalf("phone filter: %#v", filtered)
	}
}

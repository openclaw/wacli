package app

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/steipete/wacli/internal/out"
	"go.mau.fi/whatsmeow/types"
)

func TestSyncEventsOutputStaysNDJSONDuringProgress(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "123", Server: types.DefaultUserServer}
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	ids := make([]string, 25)
	for i := range ids {
		ids[i] = "m" + string(rune('a'+i))
	}
	f.connectEvents = []interface{}{historySyncWithTextMessages(chat, base, ids...)}

	raw := captureStderr(t, func() {
		a.opts.Events = out.NewEventWriter(os.Stderr, true)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, err := a.Sync(ctx, SyncOptions{
			Mode:         SyncModeOnce,
			AllowQR:      false,
			IdleExit:     50 * time.Millisecond,
			WarnNoLimits: false,
		})
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
	})

	if strings.Contains(raw, "Synced ") || strings.Contains(raw, "Processing history sync") {
		t.Fatalf("human progress leaked into --events stderr:\n%s", raw)
	}

	var sawProgress bool
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var evt map[string]any
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			t.Fatalf("stderr line is not valid JSON %q: %v\nfull stderr:\n%s", line, err, raw)
		}
		if evt["event"] == "progress" {
			data, ok := evt["data"].(map[string]any)
			if !ok || data["messages_synced"] != float64(25) {
				t.Fatalf("unexpected progress event: %#v", evt)
			}
			sawProgress = true
		}
	}
	if !sawProgress {
		t.Fatalf("expected progress event in:\n%s", raw)
	}
}

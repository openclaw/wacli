package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types/events"
)

func TestQuickReplyResyncPersistsFullReplayAndSettlesDebt(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(fmt.Sprint(empty), func(t *testing.T) {
			a, f := newQuickWriteApp(t)
			if err := a.db.MarkAppStateRecoveryRequired("regular"); err != nil {
				t.Fatal(err)
			}
			f.appStateFetchEvent = func(name string, full, only bool) any {
				if name != "regular" || !full || only {
					t.Fatalf("fetch=%s %t %t", name, full, only)
				}
				select {
				case <-a.chatStateSync:
					a.chatStateSync <- struct{}{}
					t.Fatal("replay did not own coordinator")
				default:
				}
				if empty {
					return nil
				}
				return quickReplyEvent("1", "hello", "from phone", time.Unix(100, 0), false)
			}
			got, err := a.ResyncAppState(t.Context(), "regular")
			want := 1
			if empty {
				want = 0
			}
			if err != nil || got.Collection != "regular" || got.Completeness != "complete" || got.QuickReplies != want {
				t.Fatalf("resync=%+v %v", got, err)
			}
			if pending, err := a.db.AppStateRecoveryRequired("regular"); err != nil || pending {
				t.Fatalf("debt=%v %v", pending, err)
			}
			if len(f.appStatePatches) != 0 {
				t.Fatal("resync sent a mutation")
			}
		})
	}
}

func TestQuickReplyResyncRefusesBrokenCollectionWithoutOverride(t *testing.T) {
	a, f := newQuickWriteApp(t)
	if err := a.db.SetAppStateFullSyncBroken("regular", true); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ResyncAppState(t.Context(), "regular"); err == nil || !strings.Contains(err.Error(), "primary recovery") || strings.Contains(err.Error(), "--force") {
		t.Fatal(err)
	}
	if _, err := a.ResyncAppState(t.Context(), "regular_low"); err == nil {
		t.Fatal("accepted other collection")
	}
	if len(f.appStateFetches) != 0 {
		t.Fatal("refusal fetched")
	}
	if pending, err := a.db.AppStateRecoveryRequired("regular"); err != nil || pending {
		t.Fatalf("refusal created debt=%v %v", pending, err)
	}
}

func TestQuickReplyFailedFullReplayKeepsDebtAndIncompleteCoverage(t *testing.T) {
	for _, storageFailure := range []bool{false, true} {
		t.Run(fmt.Sprint(storageFailure), func(t *testing.T) {
			a, f := newQuickWriteApp(t)
			if storageFailure {
				raw, err := sql.Open("sqlite3", filepath.Join(a.StoreDir(), "wacli.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer raw.Close()
				if _, err := raw.Exec(`CREATE TRIGGER fail_reply BEFORE INSERT ON quick_replies BEGIN SELECT RAISE(FAIL, 'injected'); END`); err != nil {
					t.Fatal(err)
				}
				w := &appStateContextWA{fakeWA: f}
				w.fetchEvents = func(context.Context, string, bool, bool) ([]any, error) {
					return []any{quickReplyEvent("1", "x", "y", time.Unix(1, 0), false), &events.AppStateSyncComplete{Name: appstate.WAPatchRegular}}, nil
				}
				a.wa = w
			} else {
				f.appStateFetchErr = errors.New("injected fetch failure")
			}
			if _, err := a.ResyncAppState(t.Context(), "regular"); err == nil {
				t.Fatal("replay should fail")
			}
			if pending, err := a.db.AppStateRecoveryRequired("regular"); err != nil || !pending {
				t.Fatalf("lost debt=%v %v", pending, err)
			}
			if got, err := a.db.QuickReplies(); err != nil || got.Completeness != "incomplete" {
				t.Fatalf("failed replay coverage=%+v %v", got, err)
			}
		})
	}
}

func TestQuickReplyResyncUsesPrimaryRecoveryAfterMismatch(t *testing.T) {
	a, f := newQuickWriteApp(t)
	f.appStateFetchErr = appstate.ErrMismatchingLTHash
	f.onAppStateRecovery = func(name string) {
		f.emit(quickReplyEvent("1", "recovered", "text", time.Unix(1, 0), false))
		f.emit(&events.AppStateSyncComplete{Name: appstate.WAPatchRegular, Recovery: true})
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	got, err := a.ResyncAppState(ctx, "regular")
	if err != nil || got.Completeness != "complete" || got.QuickReplies != 1 {
		t.Fatalf("recovery=%+v %v", got, err)
	}
}

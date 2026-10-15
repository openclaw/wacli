package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func exactRetryFixture(t *testing.T) (*App, *fakeWA, string) {
	t.Helper()
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	chat := "111@s.whatsapp.net"
	if err := a.db.UpsertChat(chat, "dm", "Synthetic", time.Now()); err != nil {
		t.Fatal(err)
	}
	insertMediaMessage(t, a, chat, "target", time.Now().Add(-time.Hour))
	return a, f, chat
}

func TestRetryMediaMessageSelectsExactIdentityDespiteLocalPath(t *testing.T) {
	a, f, chat := exactRetryFixture(t)
	insertMediaMessage(t, a, chat, "newer", time.Now())
	other := "222@s.whatsapp.net"
	if err := a.db.UpsertChat(other, "dm", "Other", time.Now()); err != nil {
		t.Fatal(err)
	}
	insertMediaMessage(t, a, other, "target", time.Now())
	// A stale local path removes this row from batch queries but must not hide
	// the explicitly requested message from phone recovery.
	if err := a.db.MarkMediaDownloaded(chat, "target", filepath.Join(t.TempDir(), "missing"), time.Now()); err != nil {
		t.Fatal(err)
	}
	f.onMediaRetry = func(info *types.MessageInfo, key []byte) any {
		if info.Chat.String() != chat || info.ID != "target" {
			t.Fatalf("wrong receipt: %+v", info)
		}
		return notOnPhoneHook(info, key)
	}
	out, err := a.RetryMediaMessage(context.Background(), chat, "target", time.Millisecond)
	if err != nil || out.Status != "recovered" || out.ChatJID != chat || out.MsgID != "target" || len(f.mediaRetryReceipts) != 1 {
		t.Fatalf("outcome=%+v error=%v receipts=%v", out, err, f.mediaRetryReceipts)
	}
	for _, target := range [][2]string{{chat, "newer"}, {other, "target"}} {
		info, err := a.db.GetMediaDownloadInfo(target[0], target[1])
		if err != nil || info.LocalPath != "" {
			t.Fatalf("unrelated message changed: %+v error=%v", info, err)
		}
	}
	// Existing corrupt bytes are also irrelevant to exact phone selection.
	corrupt := filepath.Join(t.TempDir(), "corrupt")
	if err := os.WriteFile(corrupt, []byte("invalid media bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.db.MarkMediaDownloaded(chat, "target", corrupt, time.Now()); err != nil {
		t.Fatal(err)
	}
	out, err = a.RetryMediaMessage(context.Background(), chat, "target", time.Millisecond)
	if err != nil || out.Status != "recovered" || len(f.mediaRetryReceipts) != 2 {
		t.Fatalf("corrupt cache selection outcome=%+v error=%v receipts=%v", out, err, f.mediaRetryReceipts)
	}
}

func TestRetryMediaMessageIgnoresUnrelatedNotificationsAndSendsOnce(t *testing.T) {
	for _, wrong := range []string{"chat", "id", "none"} {
		t.Run(wrong, func(t *testing.T) {
			a, f, chat := exactRetryFixture(t)
			f.onMediaRetry = func(info *types.MessageInfo, _ []byte) any {
				if wrong == "none" {
					return nil
				}
				evt := &events.MediaRetry{ChatID: info.Chat, MessageID: info.ID, Error: &events.MediaRetryError{Code: 2}}
				if wrong == "chat" {
					evt.ChatID = types.NewJID("222", types.DefaultUserServer)
				} else {
					evt.MessageID = "other"
				}
				return evt
			}
			out, err := a.RetryMediaMessage(context.Background(), chat, "target", 10*time.Millisecond)
			if err != nil || out.Status != "no_response" || len(f.mediaRetryReceipts) != 1 {
				t.Fatalf("outcome=%+v error=%v receipts=%v", out, err, f.mediaRetryReceipts)
			}
		})
	}
}

func TestRetryMediaMessageAcceptsMatchingResponseAfterUnrelatedEvents(t *testing.T) {
	a, f, chat := exactRetryFixture(t)
	f.downloadErr = whatsmeow.ErrMediaDownloadFailedWith404
	f.onMediaRetry = func(info *types.MessageInfo, key []byte) any {
		// Earlier notifications for either half of the identity must not consume
		// the matching response or cause a download for an unrelated message.
		f.emit(&events.MediaRetry{ChatID: types.NewJID("222", types.DefaultUserServer), MessageID: info.ID, Error: &events.MediaRetryError{Code: 9}})
		f.emit(&events.MediaRetry{ChatID: info.Chat, MessageID: "other", Error: &events.MediaRetryError{Code: 9}})
		return notOnPhoneHook(info, key)
	}
	out, err := a.RetryMediaMessage(context.Background(), chat, "target", time.Millisecond)
	if err != nil || out.Status != "not_on_phone" || len(f.mediaRetryReceipts) != 1 {
		t.Fatalf("correlated outcome=%+v error=%v receipts=%v", out, err, f.mediaRetryReceipts)
	}
}

func TestRetryMediaMessagePhoneGoneRequiresExpiredCDN(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		err          error
	}{
		{"expired", "not_on_phone", whatsmeow.ErrMediaDownloadFailedWith410},
		{"transient", "error", errors.New("temporary network failure")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, f, chat := exactRetryFixture(t)
			f.onMediaRetry, f.downloadErr = notOnPhoneHook, tc.err
			out, err := a.RetryMediaMessage(context.Background(), chat, "target", time.Millisecond)
			if err != nil || out.Status != tc.status {
				t.Fatalf("outcome=%+v error=%v", out, err)
			}
			n, err := a.db.CountPendingMediaDownloads(context.Background(), chat)
			want := 1
			if tc.status == "not_on_phone" {
				want = 0
			}
			if err != nil || n != want {
				t.Fatalf("pending=%d want=%d error=%v", n, want, err)
			}
		})
	}
}

// Capture the effective deadline without waiting thirty seconds in the test.
type exactDeadlineWA struct {
	*fakeWA
	deadline time.Time
}

func (f *exactDeadlineWA) SendMediaRetryReceipt(ctx context.Context, info *types.MessageInfo, key []byte) error {
	f.deadline, _ = ctx.Deadline()
	return f.fakeWA.SendMediaRetryReceipt(ctx, info, key)
}

func TestRetryMediaMessageWaitCapAndCallerDeadline(t *testing.T) {
	a, base, chat := exactRetryFixture(t)
	f := &exactDeadlineWA{fakeWA: base}
	a.wa = f
	base.onMediaRetry = notOnPhoneHook
	start := time.Now()
	if _, err := a.RetryMediaMessage(context.Background(), chat, "target", time.Hour); err != nil {
		t.Fatal(err)
	}
	if f.deadline.IsZero() || f.deadline.After(start.Add(30*time.Second+100*time.Millisecond)) || f.deadline.Before(start.Add(29*time.Second)) {
		t.Fatalf("uncapped deadline: %v", f.deadline)
	}
	base.onMediaRetry = nil
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	want, _ := ctx.Deadline()
	start = time.Now()
	_, err := a.RetryMediaMessage(ctx, chat, "target", time.Hour)
	if !errors.Is(err, context.DeadlineExceeded) || !f.deadline.Equal(want) || time.Since(start) > time.Second {
		t.Fatalf("caller deadline lost: deadline=%v want=%v error=%v", f.deadline, want, err)
	}
}

func TestRetryMediaMessageRejectsMissingSelector(t *testing.T) {
	a, f, chat := exactRetryFixture(t)
	for _, pair := range [][2]string{{"", "target"}, {chat, " "}, {" ", "target"}} {
		if _, err := a.RetryMediaMessage(context.Background(), pair[0], pair[1], time.Millisecond); err == nil {
			t.Fatalf("accepted selector %v", pair)
		}
	}
	if _, err := a.RetryMediaMessage(context.Background(), chat, "target", -time.Second); err == nil {
		t.Fatal("accepted negative wait")
	}
	if len(f.mediaRetryReceipts) != 0 {
		t.Fatalf("invalid input sent receipts: %v", f.mediaRetryReceipts)
	}
	out, err := a.RetryMediaMessage(context.Background(), chat, "missing", time.Millisecond)
	if err != nil || out.Status != "error" || len(f.mediaRetryReceipts) != 0 {
		t.Fatalf("missing message outcome=%+v error=%v receipts=%v", out, err, f.mediaRetryReceipts)
	}
}

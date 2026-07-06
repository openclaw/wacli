package app

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func notOnPhoneHook(info *types.MessageInfo, _ []byte) interface{} {
	return &events.MediaRetry{MessageID: types.MessageID(info.ID), ChatID: info.Chat, Error: &events.MediaRetryError{Code: 2}}
}

func TestRetryMediaMarksNotOnPhone(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	f.onMediaRetry = notOnPhoneHook
	f.downloadErr = whatsmeow.ErrMediaDownloadFailedWith403

	chat := "123@s.whatsapp.net"
	if err := a.db.UpsertChat(chat, "dm", "Alice", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	base := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	for _, id := range []string{"m1", "m2", "m3"} {
		insertMediaMessage(t, a, chat, id, base)
	}

	res, err := a.RetryMedia(context.Background(), RetryMediaOptions{Wait: 2 * time.Second})
	if err != nil {
		t.Fatalf("RetryMedia: %v", err)
	}
	if res.Requested != 3 || res.NotOnPhone != 3 || res.Recovered != 0 || res.NoResponse != 0 || res.Failed != 0 {
		t.Fatalf("unexpected result: %+v", res)
	}
	// Marked-gone media must drop out of the pending set.
	if n, err := a.db.CountPendingMediaDownloads(context.Background(), ""); err != nil || n != 0 {
		t.Fatalf("expected 0 pending after marking gone, got %d (err %v)", n, err)
	}
	// A second run finds nothing to do.
	res2, err := a.RetryMedia(context.Background(), RetryMediaOptions{Wait: 2 * time.Second})
	if err != nil {
		t.Fatalf("RetryMedia rerun: %v", err)
	}
	if res2.Requested != 0 {
		t.Fatalf("expected nothing pending on rerun, got %+v", res2)
	}
}

func TestRetryMediaNoResponseDoesNotMarkGone(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	f.onMediaRetry = nil // phone never answers

	chat := "123@s.whatsapp.net"
	if err := a.db.UpsertChat(chat, "dm", "Alice", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	insertMediaMessage(t, a, chat, "m1", time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC))

	res, err := a.RetryMedia(context.Background(), RetryMediaOptions{Wait: 150 * time.Millisecond})
	if err != nil {
		t.Fatalf("RetryMedia: %v", err)
	}
	if res.NoResponse != 1 || res.NotOnPhone != 0 || res.Recovered != 0 {
		t.Fatalf("expected 1 no_response, got %+v", res)
	}
	// A non-responder must stay pending (not marked gone) so it can be retried.
	if n, err := a.db.CountPendingMediaDownloads(context.Background(), ""); err != nil || n != 1 {
		t.Fatalf("expected 1 still pending, got %d (err %v)", n, err)
	}
	// A non-responder gets a second attempt within the run.
	if len(f.mediaRetryReceipts) != 2 {
		t.Fatalf("expected 2 receipts (initial + second attempt), got %d", len(f.mediaRetryReceipts))
	}
}

func TestRetryMediaBatches(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	f.onMediaRetry = notOnPhoneHook
	f.downloadErr = whatsmeow.ErrMediaDownloadFailedWith403

	chat := "123@s.whatsapp.net"
	if err := a.db.UpsertChat(chat, "dm", "Alice", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	base := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	for i, id := range []string{"m1", "m2", "m3", "m4", "m5"} {
		insertMediaMessage(t, a, chat, id, base.Add(time.Duration(i)*time.Second))
	}

	res, err := a.RetryMedia(context.Background(), RetryMediaOptions{BatchSize: 2, Wait: 2 * time.Second})
	if err != nil {
		t.Fatalf("RetryMedia: %v", err)
	}
	if res.Requested != 5 || res.NotOnPhone != 5 {
		t.Fatalf("expected all 5 marked not_on_phone across batches, got %+v", res)
	}
	// Each message answered on the first attempt, so exactly one receipt each.
	if len(f.mediaRetryReceipts) != 5 {
		t.Fatalf("expected 5 receipts, got %d", len(f.mediaRetryReceipts))
	}
}

func TestRetryMediaScopesDuplicateMessageIDsByChat(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	f.onMediaRetry = notOnPhoneHook
	f.downloadErr = whatsmeow.ErrMediaDownloadFailedWith403

	for _, chat := range []string{"111@s.whatsapp.net", "222@s.whatsapp.net"} {
		if err := a.db.UpsertChat(chat, "dm", "Chat", time.Now()); err != nil {
			t.Fatalf("UpsertChat: %v", err)
		}
		insertMediaMessage(t, a, chat, "same-id", time.Now())
	}

	res, err := a.RetryMedia(context.Background(), RetryMediaOptions{Wait: time.Second})
	if err != nil {
		t.Fatalf("RetryMedia: %v", err)
	}
	if res.Requested != 2 || res.NotOnPhone != 2 || res.NoResponse != 0 || res.Failed != 0 {
		t.Fatalf("unexpected duplicate-ID result: %+v", res)
	}
}

func TestRetryMediaUsesCDNFallbackBeforeMarkingUnavailable(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	f.onMediaRetry = notOnPhoneHook

	chat := "123@s.whatsapp.net"
	if err := a.db.UpsertChat(chat, "dm", "Alice", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	insertMediaMessage(t, a, chat, "m1", time.Now())

	res, err := a.RetryMedia(context.Background(), RetryMediaOptions{Wait: time.Second})
	if err != nil {
		t.Fatalf("RetryMedia: %v", err)
	}
	if res.Recovered != 1 || res.NotOnPhone != 0 || res.Failed != 0 {
		t.Fatalf("unexpected fallback result: %+v", res)
	}
	info, err := a.db.GetMediaDownloadInfo(chat, "m1")
	if err != nil {
		t.Fatalf("GetMediaDownloadInfo: %v", err)
	}
	if info.LocalPath == "" {
		t.Fatalf("expected CDN fallback to record local path")
	}
}

func TestRetryMediaDoesNotMarkUnavailableOnTransientCDNFailure(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	f.onMediaRetry = notOnPhoneHook
	f.downloadErr = errors.New("temporary CDN failure")

	chat := "123@s.whatsapp.net"
	if err := a.db.UpsertChat(chat, "dm", "Alice", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	insertMediaMessage(t, a, chat, "m1", time.Now())

	res, err := a.RetryMedia(context.Background(), RetryMediaOptions{Wait: time.Second})
	if err != nil {
		t.Fatalf("RetryMedia: %v", err)
	}
	if res.Failed != 1 || res.NotOnPhone != 0 {
		t.Fatalf("unexpected transient-failure result: %+v", res)
	}
	if n, err := a.db.CountPendingMediaDownloads(context.Background(), ""); err != nil || n != 1 {
		t.Fatalf("expected row to remain pending, got %d (err %v)", n, err)
	}
}

func TestRetryMediaBeforeFilter(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	f.onMediaRetry = notOnPhoneHook
	f.downloadErr = whatsmeow.ErrMediaDownloadFailedWith403

	chat := "123@s.whatsapp.net"
	if err := a.db.UpsertChat(chat, "dm", "Alice", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	base := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	insertMediaMessage(t, a, chat, "old", base)
	insertMediaMessage(t, a, chat, "new", base.Add(2*time.Hour))

	res, err := a.RetryMedia(context.Background(), RetryMediaOptions{BeforeUnix: base.Add(time.Hour).Unix(), BeforeSet: true, Wait: time.Second})
	if err != nil {
		t.Fatalf("RetryMedia: %v", err)
	}
	if res.Requested != 1 || res.NotOnPhone != 1 {
		t.Fatalf("unexpected before-filter result: %+v", res)
	}
	if n, err := a.db.CountPendingMediaDownloads(context.Background(), ""); err != nil || n != 1 {
		t.Fatalf("expected newer row to remain pending, got %d (err %v)", n, err)
	}
}

func TestRetryMediaExplicitEpochBeforeFilterDoesNotRetryEverything(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := "123@s.whatsapp.net"
	if err := a.db.UpsertChat(chat, "dm", "Alice", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	insertMediaMessage(t, a, chat, "m1", time.Now())

	res, err := a.RetryMedia(context.Background(), RetryMediaOptions{BeforeUnix: 0, BeforeSet: true, Wait: time.Second})
	if err != nil {
		t.Fatalf("RetryMedia: %v", err)
	}
	if res.Requested != 0 || len(f.mediaRetryReceipts) != 0 {
		t.Fatalf("explicit epoch filter retried media: result=%+v receipts=%d", res, len(f.mediaRetryReceipts))
	}
}

func TestRetryMediaMatchesLIDNotificationToCanonicalChat(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	f.downloadErr = whatsmeow.ErrMediaDownloadFailedWith403
	pn := types.NewJID("123", types.DefaultUserServer)
	lid := types.NewJID("999", types.HiddenUserServer)
	f.lids[lid] = pn
	f.onMediaRetry = func(info *types.MessageInfo, _ []byte) interface{} {
		return &events.MediaRetry{MessageID: types.MessageID(info.ID), ChatID: lid, Error: &events.MediaRetryError{Code: 2}}
	}

	if err := a.db.UpsertChat(pn.String(), "dm", "Alice", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	insertMediaMessage(t, a, pn.String(), "m1", time.Now())

	res, err := a.RetryMedia(context.Background(), RetryMediaOptions{Wait: time.Second})
	if err != nil {
		t.Fatalf("RetryMedia: %v", err)
	}
	if res.NotOnPhone != 1 || res.NoResponse != 0 || res.Failed != 0 {
		t.Fatalf("unexpected LID notification result: %+v", res)
	}
}

func TestRetryMediaTreatsBroadcastListsAsGroupLike(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	f.downloadErr = whatsmeow.ErrMediaDownloadFailedWith403
	var receiptSource types.MessageSource
	f.onMediaRetry = func(info *types.MessageInfo, _ []byte) interface{} {
		receiptSource = info.MessageSource
		return &events.MediaRetry{MessageID: types.MessageID(info.ID), ChatID: info.Chat, Error: &events.MediaRetryError{Code: 2}}
	}

	chat := types.NewJID("123", types.BroadcastServer)
	if err := a.db.UpsertChat(chat.String(), "broadcast", "List", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := a.db.UpsertMessage(store.UpsertMessageParams{
		ChatJID: chat.String(), MsgID: "m1", Timestamp: time.Now(), FromMe: true,
		MediaType: "image", DirectPath: "/direct/m1", MediaKey: []byte{1},
	}); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}

	res, err := a.RetryMedia(context.Background(), RetryMediaOptions{Wait: time.Second})
	if err != nil {
		t.Fatalf("RetryMedia: %v", err)
	}
	if res.NotOnPhone != 1 || res.Failed != 0 {
		t.Fatalf("unexpected broadcast result: %+v", res)
	}
	wantSender, _ := types.ParseJID(f.LinkedJID())
	if !receiptSource.IsGroup || receiptSource.Sender != wantSender {
		t.Fatalf("broadcast receipt source = %+v, want group-like sender %s", receiptSource, wantSender)
	}
}

func TestRetryMediaUsesOwnSenderForOutgoingGroupMedia(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	f.downloadErr = whatsmeow.ErrMediaDownloadFailedWith403
	var receiptSender types.JID
	f.onMediaRetry = func(info *types.MessageInfo, _ []byte) interface{} {
		receiptSender = info.Sender
		return &events.MediaRetry{MessageID: types.MessageID(info.ID), ChatID: info.Chat, Error: &events.MediaRetryError{Code: 2}}
	}

	chat := "123@g.us"
	groupJID, _ := types.ParseJID(chat)
	f.groups[groupJID] = &types.GroupInfo{JID: groupJID, AddressingMode: types.AddressingModePN}
	if err := a.db.UpsertChat(chat, "group", "Group", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := a.db.UpsertMessage(store.UpsertMessageParams{
		ChatJID: chat, MsgID: "m1", Timestamp: time.Now(), FromMe: true,
		MediaType: "image", DirectPath: "/direct/m1", MediaKey: []byte{1},
	}); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}

	res, err := a.RetryMedia(context.Background(), RetryMediaOptions{Wait: time.Second})
	if err != nil {
		t.Fatalf("RetryMedia: %v", err)
	}
	if res.NotOnPhone != 1 || res.Failed != 0 {
		t.Fatalf("unexpected group result: %+v", res)
	}
	want, _ := types.ParseJID(f.LinkedJID())
	if receiptSender != want {
		t.Fatalf("receipt sender = %s, want %s", receiptSender, want)
	}
}

func TestRetryMediaRejectsNegativeOptions(t *testing.T) {
	a := newTestApp(t)
	for _, opts := range []RetryMediaOptions{{Limit: -1}, {BatchSize: -1}, {Wait: -1}} {
		if _, err := a.RetryMedia(context.Background(), opts); err == nil {
			t.Fatalf("expected error for options %+v", opts)
		}
	}
}

func TestIsExpiredMediaDownload(t *testing.T) {
	if !isExpiredMediaDownload(fmt.Errorf("wrapped: %w", whatsmeow.ErrMediaDownloadFailedWith410)) {
		t.Fatalf("expected wrapped 410 to be expired")
	}
	if isExpiredMediaDownload(errors.New("timeout")) {
		t.Fatalf("unexpected transient error classified as expired")
	}
}

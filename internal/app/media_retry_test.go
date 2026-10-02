package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func notOnPhoneHook(info *types.MessageInfo, _ []byte) any {
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

func TestRetryMediaTypeFilter(t *testing.T) {
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
	insertMediaMessage(t, a, chat, "photo", base.Add(time.Hour))
	for _, id := range []string{"voice1", "voice2"} {
		if err := a.db.UpsertMessage(store.UpsertMessageParams{
			ChatJID: chat, MsgID: id, SenderJID: chat, Timestamp: base,
			MediaType: "audio", MimeType: "audio/ogg; codecs=opus",
			DirectPath: "/direct/" + id, MediaKey: []byte{1},
		}); err != nil {
			t.Fatalf("UpsertMessage %s: %v", id, err)
		}
	}

	res, err := a.RetryMedia(context.Background(), RetryMediaOptions{MediaType: " audio ", Wait: time.Second})
	if err != nil {
		t.Fatalf("RetryMedia: %v", err)
	}
	if res.Requested != 2 || res.NotOnPhone != 2 || res.Failed != 0 {
		t.Fatalf("unexpected type-filter result: %+v", res)
	}
	for _, id := range f.mediaRetryReceipts {
		if id == "photo" {
			t.Fatalf("receipt sent for filtered-out image: %v", f.mediaRetryReceipts)
		}
	}
	// The image was never asked for, so it stays pending for a later run.
	if n, err := a.db.CountPendingMediaDownloads(context.Background(), ""); err != nil || n != 1 {
		t.Fatalf("expected the image to remain pending, got %d (err %v)", n, err)
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
	f.onMediaRetry = func(info *types.MessageInfo, _ []byte) any {
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
	f.onMediaRetry = func(info *types.MessageInfo, _ []byte) any {
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
	f.onMediaRetry = func(info *types.MessageInfo, _ []byte) any {
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

type reuploadedMediaWA struct {
	*fakeWA
	normalCalls int
	encHash     []byte
	retryCalls  int
	fileHash    []byte
	mediaKey    []byte
}

func (f *reuploadedMediaWA) DownloadMediaToFile(ctx context.Context, path string, encHash, fileHash, key []byte, length uint64, mediaType, mmsType, target string) (int64, error) {
	f.normalCalls++
	f.encHash = bytes.Clone(encHash)
	return 0, whatsmeow.ErrInvalidMediaEncSHA256
}

func (f *reuploadedMediaWA) DownloadRetriedMediaToFile(ctx context.Context, path string, fileHash, key []byte, length uint64, mediaType, target string) (int64, error) {
	f.retryCalls++
	if !bytes.Equal(fileHash, f.fileHash) || !bytes.Equal(key, f.mediaKey) {
		return 0, fmt.Errorf("lost authenticated media identity")
	}
	return f.fakeWA.DownloadMediaToFile(ctx, path, nil, fileHash, key, length, mediaType, "", target)
}

func TestRetryMediaDoesNotReuseOriginalCiphertextHash(t *testing.T) {
	for _, samePath := range []bool{false, true} {
		t.Run(fmt.Sprintf("same-path-%t", samePath), func(t *testing.T) {
			a := newTestApp(t)
			chat := "15550000001@s.whatsapp.net"
			if err := a.db.UpsertChat(chat, "dm", "Synthetic", time.Now()); err != nil {
				t.Fatal(err)
			}
			insertMediaMessage(t, a, chat, "retry", time.Now())
			info, err := a.db.GetMediaDownloadInfo(chat, "retry")
			if err != nil {
				t.Fatal(err)
			}
			info.FileEncSHA256 = bytes.Repeat([]byte{1}, 32)
			info.FileSHA256 = bytes.Repeat([]byte{2}, 32)
			info.MediaKey = bytes.Repeat([]byte{3}, 32)
			f := &reuploadedMediaWA{fakeWA: newFakeWA(), fileHash: info.FileSHA256, mediaKey: info.MediaKey}
			a.wa = f
			path := "/reuploaded"
			if samePath {
				path = info.DirectPath
			}
			var result MediaRetryResult
			got := a.classifyRetry(context.Background(), info, "retry", retryNotif{directPath: path, code: wa.MediaRetrySuccess}, true, &result)
			if got.Status != "recovered" || result.Recovered != 1 || f.retryCalls != 1 || f.normalCalls != 0 {
				t.Fatalf("retry outcome=%+v counts=%+v downloads=%d/%d", got, result, f.normalCalls, f.retryCalls)
			}
		})
	}
}

func TestRetryMediaStoredPathFallbackRetainsCiphertextHash(t *testing.T) {
	a := newTestApp(t)
	f := &reuploadedMediaWA{fakeWA: newFakeWA()}
	a.wa = f
	info := store.MediaDownloadInfo{ChatJID: "15550000001@s.whatsapp.net", MsgID: "missing", DirectPath: "/original", MediaType: "image", FileEncSHA256: bytes.Repeat([]byte{9}, 32)}
	var result MediaRetryResult
	got := a.classifyRetry(context.Background(), info, info.MsgID, retryNotif{code: wa.MediaRetryNotFound}, true, &result)
	if got.Status != "error" || f.normalCalls != 1 || f.retryCalls != 0 || !bytes.Equal(f.encHash, info.FileEncSHA256) {
		t.Fatalf("fallback=%+v downloads=%d/%d", got, f.normalCalls, f.retryCalls)
	}
}

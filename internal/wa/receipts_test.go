package wa

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"
)

func TestMarkReadRequiresConnection(t *testing.T) {
	var c Client
	chat := types.NewJID("123", types.DefaultUserServer)
	receipt, err := c.MarkRead(context.Background(), []types.MessageID{"id"}, time.Now(), chat, types.EmptyJID, "")
	if err == nil || !strings.Contains(err.Error(), "not connected") || receipt != "" {
		t.Fatalf("MarkRead without a client = %q, %v; want not connected", receipt, err)
	}
}

func TestEffectiveReadReceiptType(t *testing.T) {
	dm := types.NewJID("123", types.DefaultUserServer)
	group := types.NewJID("120363000000000001", types.GroupServer)
	newsletter := types.NewJID("123", types.NewsletterServer)
	for _, tc := range []struct {
		name     string
		chat     types.JID
		receipts types.PrivacySetting
		want     types.ReceiptType
	}{
		{name: "receipts on", chat: dm, receipts: types.PrivacySettingAll, want: types.ReceiptTypeRead},
		{name: "receipts off", chat: dm, receipts: types.PrivacySettingNone, want: types.ReceiptTypeReadSelf},
		// whatsmeow hides receipts from senders in every chat kind, groups included.
		{name: "group with receipts off", chat: group, receipts: types.PrivacySettingNone, want: types.ReceiptTypeReadSelf},
		{name: "group with receipts on", chat: group, receipts: types.PrivacySettingAll, want: types.ReceiptTypeRead},
		// An unfetched setting is not "none": whatsmeow treats it as receipts on.
		{name: "setting undefined", chat: dm, receipts: types.PrivacySettingUndefined, want: types.ReceiptTypeRead},
		{name: "newsletter", chat: newsletter, receipts: types.PrivacySettingAll, want: types.ReceiptTypeReadSelf},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := effectiveReadReceiptType(tc.chat, types.PrivacySettings{ReadReceipts: tc.receipts})
			if got != tc.want {
				t.Fatalf("effectiveReadReceiptType = %q, want %q", got, tc.want)
			}
		})
	}
}

type receiptTransportFake struct {
	privacy  types.PrivacySetting
	fetchErr error
	fetches  int
	requests []types.ReceiptType
}

func (f *receiptTransportFake) TryFetchPrivacySettings(context.Context, bool) (*types.PrivacySettings, error) {
	f.fetches++
	return &types.PrivacySettings{ReadReceipts: f.privacy}, f.fetchErr
}
func (f *receiptTransportFake) MarkRead(_ context.Context, _ []types.MessageID, _ time.Time, _, _ types.JID, kinds ...types.ReceiptType) error {
	f.requests = append(f.requests, kinds[0])
	// Simulate a concurrent privacy change during dispatch.
	if f.privacy == types.PrivacySettingAll {
		f.privacy = types.PrivacySettingNone
	} else {
		f.privacy = types.PrivacySettingAll
	}
	return nil
}
func TestReceiptOutcomeDoesNotInferDispatchFromLaterPrivacy(t *testing.T) {
	for _, tc := range []struct {
		initial types.PrivacySetting
		want    types.ReceiptType
	}{
		{types.PrivacySettingAll, ReadReceiptUnknown},
		{types.PrivacySettingNone, types.ReceiptTypeReadSelf},
	} {
		f := &receiptTransportFake{privacy: tc.initial}
		got, err := dispatchReadReceipt(context.Background(), f, []types.MessageID{"id"}, time.Now(), types.NewJID("123", types.DefaultUserServer), types.EmptyJID)
		if err != nil || got != tc.want || f.fetches != 1 {
			t.Fatalf("outcome=%s err=%v fetches=%d", got, err, f.fetches)
		}
		if tc.initial == types.PrivacySettingNone && f.requests[0] != types.ReceiptTypeReadSelf {
			t.Fatal("self-only type not pinned")
		}
	}
}
func TestReceiptPrivacyLookupFailureDoesNotDispatch(t *testing.T) {
	f := &receiptTransportFake{fetchErr: errors.New("offline")}
	if _, err := dispatchReadReceipt(context.Background(), f, []types.MessageID{"id"}, time.Now(), types.NewJID("123", types.DefaultUserServer), types.EmptyJID); err == nil {
		t.Fatal("privacy failure ignored")
	}
	if len(f.requests) != 0 {
		t.Fatal("receipt sent despite failed privacy lookup")
	}
}

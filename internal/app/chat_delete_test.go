package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types"
)

func deleteChatTestApp(t *testing.T) (*App, *fakeWA) {
	t.Helper()
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	return a, f
}

func storeChatMessage(t *testing.T, a *App, chat types.JID, id string, at time.Time, fromMe bool) {
	t.Helper()
	sender := chat.String()
	if fromMe {
		sender = ""
	}
	if err := a.db.UpsertChat(chat.String(), "dm", "Greta Sala", at); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := a.db.UpsertMessage(store.UpsertMessageParams{
		ChatJID:   chat.String(),
		MsgID:     id,
		SenderJID: sender,
		Timestamp: at,
		FromMe:    fromMe,
		Text:      "text " + id,
	}); err != nil {
		t.Fatalf("UpsertMessage %s: %v", id, err)
	}
}

func deleteChatCalls(f *fakeWA) []fakeDeleteChatCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeDeleteChatCall(nil), f.deleteChatCalls...)
}

func TestDeleteChatNamesTheNewestStoredMessageAtOrBeforeThrough(t *testing.T) {
	a, f := deleteChatTestApp(t)
	chat := types.JID{User: "15550006666", Server: types.DefaultUserServer}
	through := time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)
	storeChatMessage(t, a, chat, "theirs", through.Add(-time.Hour), false)
	storeChatMessage(t, a, chat, "mine", through.Add(-time.Minute), true)

	deleted, err := a.DeleteChat(context.Background(), chat, through.Add(750*time.Millisecond))
	if err != nil {
		t.Fatalf("DeleteChat: %v", err)
	}
	if deleted.Chat != chat || deleted.LastMessageID != "mine" || !deleted.Through.Equal(through.Add(-time.Minute)) {
		t.Fatalf("deleted = %+v, want mine at %s", deleted, through.Add(-time.Minute))
	}
	calls := deleteChatCalls(f)
	if len(calls) != 1 {
		t.Fatalf("delete calls = %+v, want one", calls)
	}
	call := calls[0]
	if call.target != chat || !call.lastMsgTS.Equal(through.Add(-time.Minute)) {
		t.Fatalf("delete call = %+v, want %s through %s", call, chat, through.Add(-time.Minute))
	}
	if key := call.lastMsgKey; key == nil || key.GetID() != "mine" || !key.GetFromMe() || key.GetRemoteJID() != chat.String() || key.GetParticipant() != "" {
		t.Fatalf("last message key = %+v, want his own message in %s", call.lastMsgKey, chat)
	}

	f.mu.Lock()
	fetches := append([]fakeAppStateFetch(nil), f.appStateFetches...)
	f.mu.Unlock()
	if len(fetches) == 0 || fetches[0].name != string(appstate.WAPatchRegularHigh) {
		t.Fatalf("app state fetches = %+v, want regular_high synced before the delete", fetches)
	}
}

func TestDeleteChatRefusesWhenTheStoreHoldsALaterMessage(t *testing.T) {
	a, f := deleteChatTestApp(t)
	chat := types.JID{User: "15550007777", Server: types.DefaultUserServer}
	through := time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)
	storeChatMessage(t, a, chat, "seen", through.Add(-time.Hour), false)
	storeChatMessage(t, a, chat, "unseen", through.Add(time.Second), false)

	_, err := a.DeleteChat(context.Background(), chat, through)
	var refusal *ChatDeleteRefusal
	if !errors.As(err, &refusal) || refusal.Code != ChatDeleteNewerMessages {
		t.Fatalf("error = %v, want a %s refusal", err, ChatDeleteNewerMessages)
	}
	if calls := deleteChatCalls(f); len(calls) != 0 {
		t.Fatalf("delete calls = %+v, want none", calls)
	}
}

func TestDeleteChatRefusesAChatTheStoreDoesNotHold(t *testing.T) {
	a, f := deleteChatTestApp(t)
	chat := types.JID{User: "15550008888", Server: types.DefaultUserServer}

	_, err := a.DeleteChat(context.Background(), chat, time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC))
	var refusal *ChatDeleteRefusal
	if !errors.As(err, &refusal) || refusal.Code != ChatDeleteNotFound {
		t.Fatalf("error = %v, want a %s refusal", err, ChatDeleteNotFound)
	}
	if calls := deleteChatCalls(f); len(calls) != 0 {
		t.Fatalf("delete calls = %+v, want none", calls)
	}
}

func TestDeleteChatRefusesEveryChatButAOneToOne(t *testing.T) {
	for name, chat := range map[string]types.JID{
		"group":      {User: "120363000000000001", Server: types.GroupServer},
		"status":     types.StatusBroadcastJID,
		"broadcast":  {User: "1700000000", Server: types.BroadcastServer},
		"newsletter": {User: "120363000000000002", Server: types.NewsletterServer},
		"bot":        {User: "13135550002", Server: types.BotServer},
	} {
		t.Run(name, func(t *testing.T) {
			a, f := deleteChatTestApp(t)
			if err := a.db.UpsertChat(chat.String(), "group", "Sailing club", time.Now()); err != nil {
				t.Fatalf("UpsertChat: %v", err)
			}
			_, err := a.DeleteChat(context.Background(), chat, time.Now())
			var refusal *ChatDeleteRefusal
			if !errors.As(err, &refusal) || refusal.Code != ChatDeleteNotOneToOne {
				t.Fatalf("error = %v, want a %s refusal", err, ChatDeleteNotOneToOne)
			}
			f.mu.Lock()
			fetches := len(f.appStateFetches)
			f.mu.Unlock()
			if calls := deleteChatCalls(f); len(calls) != 0 || fetches != 0 {
				t.Fatalf("delete calls = %+v, app state fetches = %d; want WhatsApp untouched", calls, fetches)
			}
		})
	}
}

func TestDeleteChatWithoutAStoredMessageReachesThroughTheTime(t *testing.T) {
	a, f := deleteChatTestApp(t)
	chat := types.JID{User: "15550009999", Server: types.DefaultUserServer}
	if err := a.db.UpsertChat(chat.String(), "dm", "Lia Conti", time.Time{}); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	through := time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)

	deleted, err := a.DeleteChat(context.Background(), chat, through.Add(400*time.Millisecond))
	if err != nil {
		t.Fatalf("DeleteChat: %v", err)
	}
	if deleted.LastMessageID != "" || !deleted.Through.Equal(through) {
		t.Fatalf("deleted = %+v, want no message and the time's second", deleted)
	}
	calls := deleteChatCalls(f)
	if len(calls) != 1 || calls[0].lastMsgKey != nil || !calls[0].lastMsgTS.Equal(through) {
		t.Fatalf("delete calls = %+v, want one keyless range through %s", calls, through)
	}
}

func TestDeleteChatOfALIDReadsThePhoneNumberRows(t *testing.T) {
	a, f := deleteChatTestApp(t)
	pn := types.JID{User: "15550001212", Server: types.DefaultUserServer}
	lid := types.JID{User: "200000000000002", Server: types.HiddenUserServer}
	f.lids[lid] = pn
	through := time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)
	storeChatMessage(t, a, pn, "pn-last", through.Add(-time.Minute), false)

	deleted, err := a.DeleteChat(context.Background(), lid, through)
	if err != nil {
		t.Fatalf("DeleteChat: %v", err)
	}
	if deleted.Chat != lid || deleted.LastMessageID != "pn-last" {
		t.Fatalf("deleted = %+v, want the LID asked for, through the phone number's message", deleted)
	}
	calls := deleteChatCalls(f)
	if len(calls) != 1 || calls[0].target != lid || calls[0].lastMsgKey.GetRemoteJID() != pn.String() {
		t.Fatalf("delete calls = %+v, want the LID with the stored message's key", calls)
	}

	storeChatMessage(t, a, pn, "pn-later", through.Add(time.Minute), false)
	_, err = a.DeleteChat(context.Background(), lid, through)
	var refusal *ChatDeleteRefusal
	if !errors.As(err, &refusal) || refusal.Code != ChatDeleteNewerMessages {
		t.Fatalf("error = %v, want a later phone-number message to refuse the LID's delete", err)
	}
}

func TestDeleteChatReturnsWhatsAppsError(t *testing.T) {
	a, f := deleteChatTestApp(t)
	chat := types.JID{User: "15550003434", Server: types.DefaultUserServer}
	through := time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)
	storeChatMessage(t, a, chat, "last", through.Add(-time.Minute), false)
	f.deleteChatErr = errors.New("server rejected patch")

	_, err := a.DeleteChat(context.Background(), chat, through)
	var refusal *ChatDeleteRefusal
	if err == nil || errors.As(err, &refusal) {
		t.Fatalf("error = %v, want WhatsApp's failure, not a refusal", err)
	}
}

func TestDeleteChatNeedsATime(t *testing.T) {
	a, f := deleteChatTestApp(t)
	chat := types.JID{User: "15550005656", Server: types.DefaultUserServer}
	storeChatMessage(t, a, chat, "last", time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC), false)

	if _, err := a.DeleteChat(context.Background(), chat, time.Time{}); err == nil {
		t.Fatal("DeleteChat without a time: want an error")
	}
	if calls := deleteChatCalls(f); len(calls) != 0 {
		t.Fatalf("delete calls = %+v, want none", calls)
	}
}

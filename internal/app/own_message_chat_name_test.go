package app

import (
	"context"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow/types"
)

const ownPushName = "Shop Account"

func storeChatNameMessage(t *testing.T, a *App, chat types.JID, id, pushName string, fromMe bool) {
	t.Helper()
	err := a.storeParsedMessage(context.Background(), wa.ParsedMessage{
		Chat:      chat,
		ID:        id,
		PushName:  pushName,
		FromMe:    fromMe,
		Timestamp: time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC),
		Text:      "hello",
	}, messageStoreOptions{})
	if err != nil {
		t.Fatalf("storeParsedMessage %s: %v", id, err)
	}
}

func storedChatName(t *testing.T, a *App, chat types.JID) string {
	t.Helper()
	c, err := a.db.GetChat(chat.String())
	if err != nil {
		t.Fatalf("GetChat %s: %v", chat, err)
	}
	return c.Name
}

func TestOwnMessagePushNameDoesNotNameTheChat(t *testing.T) {
	customer := types.JID{User: "51999000111", Server: types.DefaultUserServer}

	t.Run("unknown contact falls back to the JID", func(t *testing.T) {
		a := newTestApp(t)
		a.wa = newFakeWA()
		storeChatNameMessage(t, a, customer, "own-1", ownPushName, true)
		if got := storedChatName(t, a, customer); got != customer.String() {
			t.Fatalf("chat name = %q, want the JID %q", got, customer)
		}
		msg, err := a.db.GetMessage(customer.String(), "own-1")
		if err != nil {
			t.Fatalf("GetMessage: %v", err)
		}
		if msg.ChatName == ownPushName {
			t.Fatalf("message chat_name = %q, the account's own push name", msg.ChatName)
		}
	})

	t.Run("known contact names the chat", func(t *testing.T) {
		a := newTestApp(t)
		f := newFakeWA()
		f.contacts[customer] = types.ContactInfo{Found: true, FullName: "Ana Customer"}
		a.wa = f
		storeChatNameMessage(t, a, customer, "own-2", ownPushName, true)
		if got := storedChatName(t, a, customer); got != "Ana Customer" {
			t.Fatalf("chat name = %q, want the contact's name", got)
		}
	})

	t.Run("an incoming message still names the chat from its push name", func(t *testing.T) {
		a := newTestApp(t)
		a.wa = newFakeWA()
		storeChatNameMessage(t, a, customer, "in-1", "Ana", false)
		if got := storedChatName(t, a, customer); got != "Ana" {
			t.Fatalf("chat name = %q, want the sender's push name", got)
		}
	})

	t.Run("a later own message keeps the name a customer message gave", func(t *testing.T) {
		a := newTestApp(t)
		a.wa = newFakeWA()
		storeChatNameMessage(t, a, customer, "in-2", "Ana", false)
		storeChatNameMessage(t, a, customer, "own-3", ownPushName, true)
		if got := storedChatName(t, a, customer); got != "Ana" {
			t.Fatalf("chat name = %q, want it kept as Ana", got)
		}
	})

	t.Run("a stored name equal to the account's push name is kept, then replaced by a customer message", func(t *testing.T) {
		a := newTestApp(t)
		a.wa = newFakeWA()
		// What earlier versions stored for a customer who never wrote first.
		// chats.name does not say where a name came from, so it stays.
		if err := a.db.UpsertChat(customer.String(), "dm", ownPushName, time.Date(2025, 3, 1, 11, 0, 0, 0, time.UTC)); err != nil {
			t.Fatalf("seed chat: %v", err)
		}
		storeChatNameMessage(t, a, customer, "own-4", ownPushName, true)
		if got := storedChatName(t, a, customer); got != ownPushName {
			t.Fatalf("chat name = %q, want the stored %q kept", got, ownPushName)
		}
		// The customer's own push name then takes over.
		storeChatNameMessage(t, a, customer, "in-4", "Ana", false)
		if got := storedChatName(t, a, customer); got != "Ana" {
			t.Fatalf("chat name = %q, want Ana", got)
		}
	})
}

func TestOwnGroupMessagePushNameDoesNotNameTheGroup(t *testing.T) {
	t.Run("group info unavailable", func(t *testing.T) {
		a := newTestApp(t)
		a.wa = newFakeWA()
		storeChatNameMessage(t, a, testGroupJID, "g-own-1", ownPushName, true)
		if got := storedChatName(t, a, testGroupJID); got != testGroupJID.String() {
			t.Fatalf("group name = %q, want the JID %q", got, testGroupJID)
		}
	})

	t.Run("group info names the group", func(t *testing.T) {
		a := newTestApp(t)
		f := newFakeWA()
		f.groups[testGroupJID] = testGroup("Project")
		a.wa = f
		storeChatNameMessage(t, a, testGroupJID, "g-own-2", ownPushName, true)
		if got := storedChatName(t, a, testGroupJID); got != "Project" {
			t.Fatalf("group name = %q, want Project", got)
		}
	})

	t.Run("a group named like the account keeps its name when group info is unavailable", func(t *testing.T) {
		a := newTestApp(t)
		f := newFakeWA()
		f.groups[testGroupJID] = testGroup(ownPushName)
		a.wa = f
		storeChatNameMessage(t, a, testGroupJID, "g-in-2", "Anna", false)
		if got := storedChatName(t, a, testGroupJID); got != ownPushName {
			t.Fatalf("group name = %q, want %q from the group info", got, ownPushName)
		}
		delete(f.groups, testGroupJID)
		a.forgetGroupInfo(testGroupJID)
		storeChatNameMessage(t, a, testGroupJID, "g-own-4", ownPushName, true)
		if got := storedChatName(t, a, testGroupJID); got != ownPushName {
			t.Fatalf("group name = %q, want the stored %q kept", got, ownPushName)
		}
	})

	t.Run("a later own message keeps a name stored earlier", func(t *testing.T) {
		a := newTestApp(t)
		f := newFakeWA()
		f.groups[testGroupJID] = testGroup("Project")
		a.wa = f
		storeChatNameMessage(t, a, testGroupJID, "g-in-1", "Anna", false)
		delete(f.groups, testGroupJID)
		a.forgetGroupInfo(testGroupJID)
		storeChatNameMessage(t, a, testGroupJID, "g-own-3", ownPushName, true)
		if got := storedChatName(t, a, testGroupJID); got != "Project" {
			t.Fatalf("group name = %q, want it kept as Project", got)
		}
	})
}

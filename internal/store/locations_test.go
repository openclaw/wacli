package store

import (
	"database/sql"
	"errors"
	"testing"
	"time"
)

func TestUpsertAndGetMessageLocation(t *testing.T) {
	db := openTestDB(t)
	chat := "15551112222@s.whatsapp.net"

	if err := db.UpsertMessageLocation(MessageLocation{
		ChatJID:   chat,
		MsgID:     "LOC-1",
		Latitude:  51.4779,
		Longitude: -0.0015,
		Name:      "Head office",
		Address:   "1 Market Street",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := db.GetMessageLocation(chat, "LOC-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Latitude != 51.4779 || got.Longitude != -0.0015 {
		t.Fatalf("coordinates = %v,%v", got.Latitude, got.Longitude)
	}
	if got.Name != "Head office" || got.Address != "1 Market Street" {
		t.Fatalf("name = %q address = %q", got.Name, got.Address)
	}
	if got.IsLive {
		t.Fatal("expected a static pin")
	}
}

func TestUpsertMessageLocationReplacesLiveUpdates(t *testing.T) {
	db := openTestDB(t)
	chat := "15551112222@s.whatsapp.net"

	for _, lat := range []float64{10, 11, 12} {
		if err := db.UpsertMessageLocation(MessageLocation{
			ChatJID: chat, MsgID: "LIVE-1", Latitude: lat, Longitude: 5, IsLive: true,
		}); err != nil {
			t.Fatalf("upsert %v: %v", lat, err)
		}
	}

	got, err := db.GetMessageLocation(chat, "LIVE-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Latitude != 12 {
		t.Fatalf("latitude = %v want 12", got.Latitude)
	}
	if !got.IsLive {
		t.Fatal("live flag lost on update")
	}
}

func TestGetMessageLocationMissingReturnsNoRows(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.GetMessageLocation("15551112222@s.whatsapp.net", "nope"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected sql.ErrNoRows, got %v", err)
	}
}

func TestUpsertMessageLocationRejectsUnusableCoordinates(t *testing.T) {
	db := openTestDB(t)
	tests := []struct {
		name     string
		lat, lng float64
	}{
		{"latitude out of range", 91, 0},
		{"longitude out of range", 0, -181},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := db.UpsertMessageLocation(MessageLocation{
				ChatJID: "c", MsgID: "m", Latitude: tc.lat, Longitude: tc.lng,
			}); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestDeleteMessageLocation(t *testing.T) {
	db := openTestDB(t)
	chat := "15551112222@s.whatsapp.net"
	if err := db.UpsertMessageLocation(MessageLocation{
		ChatJID: chat, MsgID: "LOC-1", Latitude: 1, Longitude: 2,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := db.DeleteMessageLocation(chat, "LOC-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := db.GetMessageLocation(chat, "LOC-1"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected the row to be gone, got %v", err)
	}
}

func TestDeleteChatRemovesItsLocations(t *testing.T) {
	db := openTestDB(t)
	chat := "15551112222@s.whatsapp.net"
	other := "15553334444@s.whatsapp.net"

	if err := db.UpsertChat(chat, "dm", "Someone", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	for _, c := range []string{chat, other} {
		if err := db.UpsertMessageLocation(MessageLocation{
			ChatJID: c, MsgID: "LOC-1", Latitude: 1, Longitude: 2,
		}); err != nil {
			t.Fatalf("upsert %s: %v", c, err)
		}
	}

	if err := db.DeleteChat(chat); err != nil {
		t.Fatalf("DeleteChat: %v", err)
	}

	if _, err := db.GetMessageLocation(chat, "LOC-1"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleting a chat must drop its locations, got %v", err)
	}
	if _, err := db.GetMessageLocation(other, "LOC-1"); err != nil {
		t.Fatalf("another chat's location must survive: %v", err)
	}
}

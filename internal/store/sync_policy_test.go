package store

import "testing"

func TestSyncOptimizationPolicyRoundTrip(t *testing.T) {
	db := openTestDB(t)
	if got, err := db.SyncOptimizationPolicy(); err != nil || got.Enabled {
		t.Fatalf("default policy = %+v, %v", got, err)
	}
	want := DefaultSyncOptimizationPolicy()
	want.MaxChats = 7
	want.MaxMessagesPerChat = 3
	if err := db.SetSyncOptimizationPolicy(want); err != nil {
		t.Fatal(err)
	}
	got, err := db.SyncOptimizationPolicy()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("policy = %+v, want %+v", got, want)
	}
}

func TestSyncOptimizationSetExcludesArchived(t *testing.T) {
	db := openTestDB(t)
	p := DefaultSyncOptimizationPolicy()
	p.MaxChats = 1
	if err := db.UpsertChat("old@s.whatsapp.net", "dm", "old", nowUTC()); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertChat("new@s.whatsapp.net", "dm", "new", nowUTC().AddDate(0, 0, 1)); err != nil {
		t.Fatal(err)
	}
	if err := db.SetChatArchived("new@s.whatsapp.net", true); err != nil {
		t.Fatal(err)
	}
	ok, err := db.ChatIsInSyncOptimizationSet("old@s.whatsapp.net", p)
	if err != nil || !ok {
		t.Fatalf("old chat retained = %t, %v", ok, err)
	}
	ok, err = db.ChatIsInSyncOptimizationSet("new@s.whatsapp.net", p)
	if err != nil || ok {
		t.Fatalf("archived chat retained = %t, %v", ok, err)
	}
}

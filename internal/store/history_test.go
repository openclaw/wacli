package store

import (
	"testing"
	"time"
)

func TestListHistoryCoverage(t *testing.T) {
	db := openTestDB(t)
	base := time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC)

	ready := "ready@s.whatsapp.net"
	blocked := "blocked@s.whatsapp.net"
	group := "group@g.us"

	if err := db.UpsertChat(ready, "dm", "Ready", base.Add(3*time.Minute)); err != nil {
		t.Fatalf("UpsertChat ready: %v", err)
	}
	if err := db.UpsertChat(blocked, "dm", "Blocked", base.Add(2*time.Minute)); err != nil {
		t.Fatalf("UpsertChat blocked: %v", err)
	}
	if err := db.UpsertChat(group, "group", "Group", base.Add(time.Minute)); err != nil {
		t.Fatalf("UpsertChat group: %v", err)
	}
	for _, msg := range []struct {
		chat string
		id   string
		ts   time.Time
	}{
		{ready, "m2", base.Add(2 * time.Second)},
		{ready, "m1", base.Add(time.Second)},
		{group, "g1", base.Add(3 * time.Second)},
	} {
		if err := db.UpsertMessage(UpsertMessageParams{
			ChatJID:   msg.chat,
			MsgID:     msg.id,
			Timestamp: msg.ts,
			FromMe:    false,
			Text:      msg.id,
		}); err != nil {
			t.Fatalf("UpsertMessage %s: %v", msg.id, err)
		}
	}

	coverage, err := db.ListHistoryCoverage(ListHistoryCoverageParams{Limit: 10})
	if err != nil {
		t.Fatalf("ListHistoryCoverage: %v", err)
	}
	if len(coverage) != 2 {
		t.Fatalf("expected blocked chat hidden by default, got %+v", coverage)
	}
	byChat := map[string]HistoryCoverage{}
	for _, c := range coverage {
		byChat[c.ChatJID] = c
	}
	if byChat[ready].Status != HistoryCoverageStatusReady || byChat[ready].MessageCount != 2 {
		t.Fatalf("ready coverage = %+v", byChat[ready])
	}
	if !byChat[ready].OldestTS.Equal(base.Add(time.Second)) || !byChat[ready].NewestTS.Equal(base.Add(2*time.Second)) {
		t.Fatalf("ready time range = %+v", byChat[ready])
	}

	withBlocked, err := db.ListHistoryCoverage(ListHistoryCoverageParams{Limit: 10, IncludeBlocked: true})
	if err != nil {
		t.Fatalf("ListHistoryCoverage blocked: %v", err)
	}
	byChat = map[string]HistoryCoverage{}
	for _, c := range withBlocked {
		byChat[c.ChatJID] = c
	}
	if byChat[blocked].Status != HistoryCoverageStatusBlocked || byChat[blocked].BlockedReason != HistoryCoverageBlockedNoAnchor {
		t.Fatalf("blocked coverage = %+v", byChat[blocked])
	}

	groups, err := db.ListHistoryCoverage(ListHistoryCoverageParams{Kind: "group", Limit: 10, IncludeBlocked: true})
	if err != nil {
		t.Fatalf("ListHistoryCoverage group: %v", err)
	}
	if len(groups) != 1 || groups[0].ChatJID != group {
		t.Fatalf("group coverage = %+v", groups)
	}

	selected, err := db.ListHistoryCoverage(ListHistoryCoverageParams{ChatJIDs: []string{blocked, ready}, OnlyActionable: true, IncludeBlocked: true})
	if err != nil {
		t.Fatalf("ListHistoryCoverage actionable: %v", err)
	}
	if len(selected) != 1 || selected[0].ChatJID != ready {
		t.Fatalf("actionable coverage = %+v", selected)
	}
}

func TestListHistoryCoverageAppliesBlockedFilterBeforeLimit(t *testing.T) {
	db := openTestDB(t)
	base := time.Date(2024, 5, 3, 0, 0, 0, 0, time.UTC)

	blocked := "blocked@s.whatsapp.net"
	ready := "ready@s.whatsapp.net"
	if err := db.UpsertChat(blocked, "dm", "Blocked", base.Add(2*time.Minute)); err != nil {
		t.Fatalf("UpsertChat blocked: %v", err)
	}
	if err := db.UpsertChat(ready, "dm", "Ready", base.Add(time.Minute)); err != nil {
		t.Fatalf("UpsertChat ready: %v", err)
	}
	if err := db.UpsertMessage(UpsertMessageParams{
		ChatJID:   ready,
		MsgID:     "m1",
		Timestamp: base.Add(time.Second),
		Text:      "ready",
	}); err != nil {
		t.Fatalf("UpsertMessage ready: %v", err)
	}

	coverage, err := db.ListHistoryCoverage(ListHistoryCoverageParams{Limit: 1})
	if err != nil {
		t.Fatalf("ListHistoryCoverage: %v", err)
	}
	if len(coverage) != 1 || coverage[0].ChatJID != ready {
		t.Fatalf("coverage = %+v, want ready chat despite newer blocked row", coverage)
	}

	withBlocked, err := db.ListHistoryCoverage(ListHistoryCoverageParams{Limit: 1, IncludeBlocked: true})
	if err != nil {
		t.Fatalf("ListHistoryCoverage IncludeBlocked: %v", err)
	}
	if len(withBlocked) != 1 || withBlocked[0].ChatJID != blocked {
		t.Fatalf("withBlocked = %+v, want newer blocked chat when requested", withBlocked)
	}
}

func TestListHistoryCoverageEscapesQueryWildcards(t *testing.T) {
	db := openTestDB(t)
	when := time.Date(2024, 5, 2, 0, 0, 0, 0, time.UTC)

	if err := db.UpsertChat("literal@s.whatsapp.net", "dm", "100% literal", when); err != nil {
		t.Fatalf("UpsertChat literal: %v", err)
	}
	if err := db.UpsertChat("wildcard@s.whatsapp.net", "dm", "100X wildcard", when); err != nil {
		t.Fatalf("UpsertChat wildcard: %v", err)
	}
	for _, chat := range []string{"literal@s.whatsapp.net", "wildcard@s.whatsapp.net"} {
		if err := db.UpsertMessage(UpsertMessageParams{ChatJID: chat, MsgID: chat, Timestamp: when, Text: "x"}); err != nil {
			t.Fatalf("UpsertMessage %s: %v", chat, err)
		}
	}

	coverage, err := db.ListHistoryCoverage(ListHistoryCoverageParams{Query: "100%", Limit: 10})
	if err != nil {
		t.Fatalf("ListHistoryCoverage: %v", err)
	}
	if len(coverage) != 1 || coverage[0].ChatJID != "literal@s.whatsapp.net" {
		t.Fatalf("wildcard leak: %+v", coverage)
	}
}

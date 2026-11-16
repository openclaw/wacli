package store

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

const redeliveryChat = "123@s.whatsapp.net"

func openRedeliveryDB(t *testing.T) *DB {
	t.Helper()
	db := openTestDB(t)
	if err := db.UpsertChat(redeliveryChat, "dm", "Alice", time.Date(2025, 3, 1, 9, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	return db
}

func redeliveryParams(id, text string, ts time.Time) UpsertMessageParams {
	return UpsertMessageParams{
		ChatJID:     redeliveryChat,
		ChatName:    "Alice",
		MsgID:       id,
		SenderJID:   redeliveryChat,
		SenderName:  "Alice",
		Timestamp:   ts,
		Text:        text,
		DisplayText: text,
	}
}

func storedMessageTimestamp(t *testing.T, db *DB, id string) time.Time {
	t.Helper()
	msg, err := db.GetMessage(redeliveryChat, id)
	if err != nil {
		t.Fatalf("GetMessage %s: %v", id, err)
	}
	return msg.Timestamp
}

func upsertRedelivery(t *testing.T, db *DB, p UpsertMessageParams) {
	t.Helper()
	if err := db.UpsertMessage(p); err != nil {
		t.Fatalf("UpsertMessage %s at %s: %v", p.MsgID, p.Timestamp, err)
	}
}

func TestUpsertMessageIdenticalRedeliveryKeepsTheStoredTimestamp(t *testing.T) {
	db := openRedeliveryDB(t)
	first := time.Date(2025, 3, 1, 10, 0, 0, 0, time.UTC)

	upsertRedelivery(t, db, redeliveryParams("m1", "hello", first))
	for _, later := range []time.Duration{time.Second, 40 * time.Minute, 30 * time.Hour} {
		upsertRedelivery(t, db, redeliveryParams("m1", "hello", first.Add(later)))
		if got := storedMessageTimestamp(t, db, "m1"); !got.Equal(first) {
			t.Fatalf("timestamp after an identical redelivery +%s = %s, want the original %s", later, got, first)
		}
	}
}

func TestUpsertMessageIdenticalMediaRedeliveryKeepsTheStoredTimestamp(t *testing.T) {
	db := openRedeliveryDB(t)
	first := time.Date(2025, 3, 1, 10, 0, 0, 0, time.UTC)
	media := redeliveryParams("m1", "", first)
	media.DisplayText = "Sent image"
	media.MediaType = "image"
	media.MediaCaption = "caption"
	media.Filename = "a.jpg"
	media.MimeType = "image/jpeg"
	media.DirectPath = "/v/t/a"
	media.MediaKey = []byte{1, 2, 3}
	media.FileSHA256 = []byte{4}
	media.FileEncSHA256 = []byte{5}
	media.FileLength = 10

	upsertRedelivery(t, db, media)
	media.Timestamp = first.Add(40 * time.Minute)
	upsertRedelivery(t, db, media)
	if got := storedMessageTimestamp(t, db, "m1"); !got.Equal(first) {
		t.Fatalf("timestamp after an identical media redelivery = %s, want %s", got, first)
	}
}

func TestUpsertMessageRedeliveryKeepsConversationOrder(t *testing.T) {
	db := openRedeliveryDB(t)
	base := time.Date(2025, 3, 1, 10, 0, 0, 0, time.UTC)

	for i, id := range []string{"m1", "m2", "m3"} {
		upsertRedelivery(t, db, redeliveryParams(id, id, base.Add(time.Duration(i)*time.Minute)))
	}
	// WhatsApp delivers m1 again, much later.
	upsertRedelivery(t, db, redeliveryParams("m1", "m1", base.Add(2*time.Hour)))

	msgs, err := db.ListMessages(ListMessagesParams{ChatJID: redeliveryChat, Limit: 10, Asc: true})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	var got []string
	for _, m := range msgs {
		got = append(got, m.MsgID)
	}
	if len(got) != 3 || got[0] != "m1" || got[1] != "m2" || got[2] != "m3" {
		t.Fatalf("order after a redelivery = %v, want [m1 m2 m3]", got)
	}
}

func TestUpsertMessageRedeliveryWithoutTimestampKeepsTheStoredOne(t *testing.T) {
	db := openRedeliveryDB(t)
	first := time.Date(2025, 3, 1, 10, 0, 0, 0, time.UTC)

	upsertRedelivery(t, db, redeliveryParams("m1", "hello", first))
	upsertRedelivery(t, db, redeliveryParams("m1", "hello", time.Time{}))
	if got := storedMessageTimestamp(t, db, "m1"); !got.Equal(first) {
		t.Fatalf("timestamp = %s, want %s", got, first)
	}
}

// The rule is limited to a received message whose payload did not change.
// Everything else keeps resolving the timestamp as before.
func TestUpsertMessageTimestampUnchangedOutsideIdenticalReception(t *testing.T) {
	first := time.Date(2025, 3, 1, 10, 0, 0, 0, time.UTC)
	later := first.Add(40 * time.Minute)

	t.Run("a message from this account takes the later timestamp", func(t *testing.T) {
		db := openRedeliveryDB(t)
		own := redeliveryParams("own", "hello", first)
		own.FromMe = true
		upsertRedelivery(t, db, own)
		own.Timestamp = later
		upsertRedelivery(t, db, own)
		if got := storedMessageTimestamp(t, db, "own"); !got.Equal(later) {
			t.Fatalf("timestamp = %s, want the later %s", got, later)
		}
	})

	t.Run("changed content takes the later timestamp with its content", func(t *testing.T) {
		db := openRedeliveryDB(t)
		upsertRedelivery(t, db, redeliveryParams("m1", "(message)", first))
		upsertRedelivery(t, db, redeliveryParams("m1", "hello", later))
		msg, err := db.GetMessage(redeliveryChat, "m1")
		if err != nil {
			t.Fatalf("GetMessage: %v", err)
		}
		if msg.Text != "hello" || !msg.Timestamp.Equal(later) {
			t.Fatalf("message = text %q ts %s, want hello at %s", msg.Text, msg.Timestamp, later)
		}
	})

	t.Run("an earlier copy still cannot move the timestamp back", func(t *testing.T) {
		db := openRedeliveryDB(t)
		upsertRedelivery(t, db, redeliveryParams("m1", "hello", later))
		upsertRedelivery(t, db, redeliveryParams("m1", "hello", first))
		if got := storedMessageTimestamp(t, db, "m1"); !got.Equal(later) {
			t.Fatalf("timestamp = %s, want %s", got, later)
		}
	})
}

// A replay of stale media metadata must still lose to fresher metadata, even
// when an identical redelivery arrived in between.
func TestUpsertMessageStaleMediaReplayStillLosesToFresherMetadata(t *testing.T) {
	ctx := context.Background()
	at := func(minute int) time.Time { return time.Date(2025, 3, 1, 10, minute, 0, 0, time.UTC) }
	media := func(path string, ts time.Time) UpsertMessageParams {
		p := redeliveryParams("m1", "", ts)
		p.DisplayText = "Sent image"
		p.MediaType = "image"
		p.DirectPath = path
		p.MediaKey = []byte{1}
		return p
	}
	readState := func(t *testing.T, db *DB) (string, sql.NullInt64, time.Time) {
		t.Helper()
		var path string
		var unavailable sql.NullInt64
		var ts int64
		if err := db.sql.QueryRow(`SELECT direct_path, media_unavailable_at, ts FROM messages WHERE chat_jid = ? AND msg_id = ?`, redeliveryChat, "m1").Scan(&path, &unavailable, &ts); err != nil {
			t.Fatalf("query: %v", err)
		}
		return path, unavailable, time.Unix(ts, 0).UTC()
	}

	t.Run("old, fresh, old replay", func(t *testing.T) {
		db := openRedeliveryDB(t)
		upsertRedelivery(t, db, media("/old", at(0)))
		upsertRedelivery(t, db, media("/fresh", at(40)))
		if err := db.MarkMediaUnavailable(ctx, redeliveryChat, "m1", at(41)); err != nil {
			t.Fatalf("MarkMediaUnavailable: %v", err)
		}
		upsertRedelivery(t, db, media("/old", at(20)))
		path, unavailable, ts := readState(t, db)
		if path != "/fresh" || !unavailable.Valid || !ts.Equal(at(40)) {
			t.Fatalf("after the stale replay: path %q, unavailable set %v, ts %s; want /fresh, set, %s", path, unavailable.Valid, ts, at(40))
		}
	})

	t.Run("identical redelivery in between", func(t *testing.T) {
		db := openRedeliveryDB(t)
		upsertRedelivery(t, db, media("/old", at(0)))
		upsertRedelivery(t, db, media("/old", at(40)))
		if got := storedMessageTimestamp(t, db, "m1"); !got.Equal(at(0)) {
			t.Fatalf("timestamp after the identical redelivery = %s, want %s", got, at(0))
		}
		upsertRedelivery(t, db, media("/fresh", at(50)))
		if err := db.MarkMediaUnavailable(ctx, redeliveryChat, "m1", at(51)); err != nil {
			t.Fatalf("MarkMediaUnavailable: %v", err)
		}
		upsertRedelivery(t, db, media("/old", at(45)))
		path, unavailable, ts := readState(t, db)
		if path != "/fresh" || !unavailable.Valid || !ts.Equal(at(50)) {
			t.Fatalf("after the stale replay: path %q, unavailable set %v, ts %s; want /fresh, set, %s", path, unavailable.Valid, ts, at(50))
		}
	})
}

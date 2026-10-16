package store

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testStickerParams(name string, upload byte) StickerParams {
	digest := sha256.Sum256([]byte(name))
	return StickerParams{
		FileHash:      base64.StdEncoding.EncodeToString(digest[:]),
		DirectPath:    "/v/t62/" + name + string('a'+rune(upload)),
		MediaKey:      bytes.Repeat([]byte{upload}, 32),
		FileEncSHA256: bytes.Repeat([]byte{upload + 1}, 32),
		MimeType:      "image/webp",
		Width:         512,
		Height:        512,
		FileLength:    4096,
	}
}

func listOnlySticker(t *testing.T, db *DB, p ListStickersParams) []Sticker {
	t.Helper()
	got, err := db.ListStickers(p)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestFavoriteStickerNewestMutationWinsInEitherOrder(t *testing.T) {
	// Within one second, so second-precision ordering would tie.
	older := time.Date(2026, 10, 1, 10, 0, 0, 100_000_000, time.UTC)
	newer := older.Add(800 * time.Millisecond)
	for _, order := range []string{"in order", "replayed out of order"} {
		t.Run(order, func(t *testing.T) {
			db := openTestDB(t)
			first := testStickerParams("cat", 1)
			second := testStickerParams("cat", 2)
			apply := []struct {
				p        StickerParams
				favorite bool
				at       time.Time
			}{{first, true, older}, {second, false, newer}}
			if order != "in order" {
				apply[0], apply[1] = apply[1], apply[0]
			}
			for _, a := range apply {
				if err := db.SetFavoriteSticker(a.p, a.favorite, a.at); err != nil {
					t.Fatal(err)
				}
			}
			if got := listOnlySticker(t, db, ListStickersParams{}); len(got) != 0 {
				t.Fatalf("unfavorited sticker still listed: %+v", got)
			}
			// The newest mutation's descriptor is kept even when an older one
			// arrives later: a media key only decrypts its own upload.
			if err := db.SetFavoriteSticker(first, true, newer.Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
			got := listOnlySticker(t, db, ListStickersParams{Favorites: true})
			if len(got) != 1 || got[0].Media.DirectPath != first.DirectPath || !bytes.Equal(got[0].Media.MediaKey, first.MediaKey) {
				t.Fatalf("refavorited sticker = %+v", got)
			}
			if err := db.SetFavoriteSticker(second, true, older); err != nil {
				t.Fatal(err)
			}
			got = listOnlySticker(t, db, ListStickersParams{Favorites: true})
			if len(got) != 1 || got[0].Media.DirectPath != first.DirectPath {
				t.Fatalf("older mutation replaced the descriptor: %+v", got)
			}
			if err := db.SetFavoriteSticker(second, false, newer.Add(time.Minute-time.Millisecond)); err != nil {
				t.Fatal(err)
			}
			if got := listOnlySticker(t, db, ListStickersParams{Favorites: true}); len(got) != 1 {
				t.Fatalf("an unfavorite one millisecond older removed the favorite: %+v", got)
			}
		})
	}
}

func TestStickerWithoutDigestIsNotDownloadable(t *testing.T) {
	db := openTestDB(t)
	p := testStickerParams("bad-identity", 1)
	p.FileHash = "not-a-sha256"
	if err := db.SetFavoriteSticker(p, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	got := listOnlySticker(t, db, ListStickersParams{})
	if len(got) != 1 || got[0].Downloadable || got[0].Media.FileSHA256 != nil {
		t.Fatalf("sticker without a SHA-256 file hash = %+v, want listed but not downloadable", got)
	}
}

func TestFavoriteStickerKeepsDescriptorWhenMutationHasNone(t *testing.T) {
	db := openTestDB(t)
	full := testStickerParams("dog", 1)
	at := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	if err := db.SetFavoriteSticker(full, true, at); err != nil {
		t.Fatal(err)
	}
	// Some clients send only the flag; a partial descriptor must never land.
	partial := StickerParams{FileHash: full.FileHash, DirectPath: "/v/t62/other"}
	if err := db.SetFavoriteSticker(partial, true, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got := listOnlySticker(t, db, ListStickersParams{})
	if len(got) != 1 || got[0].Media.DirectPath != full.DirectPath || !got[0].Downloadable {
		t.Fatalf("sticker = %+v", got)
	}
	if got[0].FavoritedAt == nil || !got[0].FavoritedAt.Equal(at.Add(time.Minute)) {
		t.Fatalf("favorited_at = %v", got[0].FavoritedAt)
	}
}

func TestHistoryFavoriteStickerNeverOverridesAppState(t *testing.T) {
	db := openTestDB(t)
	unfavorited := testStickerParams("fox", 1)
	if err := db.SetFavoriteSticker(StickerParams{FileHash: unfavorited.FileHash}, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	listed := testStickerParams("owl", 3)
	for _, p := range []StickerParams{unfavorited, listed} {
		if err := db.AddHistoryFavoriteSticker(p); err != nil {
			t.Fatal(err)
		}
	}
	got := listOnlySticker(t, db, ListStickersParams{})
	if len(got) != 1 || got[0].FileHash != listed.FileHash || !got[0].Favorite || got[0].FavoritedAt != nil {
		t.Fatalf("stickers = %+v, want only the history favorite", got)
	}
	// History fills a missing descriptor but never replaces a stored one.
	if err := db.AddHistoryFavoriteSticker(testStickerParams("owl", 7)); err != nil {
		t.Fatal(err)
	}
	if got := listOnlySticker(t, db, ListStickersParams{}); got[0].Media.DirectPath != listed.DirectPath {
		t.Fatalf("history replaced a stored descriptor: %+v", got[0])
	}
}

func TestRecentStickersHonorRemovalInEitherOrder(t *testing.T) {
	sent := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	db := openTestDB(t)
	removedLater := testStickerParams("removed-later", 1)
	removedFirst := testStickerParams("removed-first", 2)
	sentAgain := testStickerParams("sent-again", 3)
	kept := testStickerParams("kept", 4)

	if err := db.AddHistoryRecentSticker(removedLater, 1, sent); err != nil {
		t.Fatal(err)
	}
	if err := db.RemoveRecentSticker(removedLater.FileHash, sent); err != nil {
		t.Fatal(err)
	}
	if err := db.RemoveRecentSticker(removedFirst.FileHash, sent); err != nil {
		t.Fatal(err)
	}
	if err := db.AddHistoryRecentSticker(removedFirst, 1, sent); err != nil {
		t.Fatal(err)
	}
	if err := db.RemoveRecentSticker(sentAgain.FileHash, sent); err != nil {
		t.Fatal(err)
	}
	if err := db.AddHistoryRecentSticker(sentAgain, 2, sent.Add(800*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if err := db.AddHistoryRecentSticker(kept, 3, sent); err != nil {
		t.Fatal(err)
	}

	got := listOnlySticker(t, db, ListStickersParams{Recent: true})
	if len(got) != 2 || got[0].FileHash != kept.FileHash || got[1].FileHash != sentAgain.FileHash {
		t.Fatalf("recent stickers = %+v, want kept then sent-again by weight", got)
	}
	if got[0].LastSentAt == nil || !got[0].LastSentAt.Equal(sent) || got[0].RecentWeight != 3 {
		t.Fatalf("recent sticker = %+v", got[0])
	}
}

func TestListStickersFiltersAndHidesDownloadMaterial(t *testing.T) {
	db := openTestDB(t)
	at := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	both := testStickerParams("both", 1)
	older := testStickerParams("older-favorite", 2)
	recentOnly := testStickerParams("recent-only", 3)
	if err := db.SetFavoriteSticker(both, true, at); err != nil {
		t.Fatal(err)
	}
	if err := db.AddHistoryRecentSticker(both, 1, at); err != nil {
		t.Fatal(err)
	}
	if err := db.SetFavoriteSticker(older, true, at.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.AddHistoryRecentSticker(recentOnly, 1, at); err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		params ListStickersParams
		want   []string
	}{
		{ListStickersParams{}, []string{both.FileHash, older.FileHash, recentOnly.FileHash}},
		{ListStickersParams{Favorites: true, Recent: true}, []string{both.FileHash, older.FileHash, recentOnly.FileHash}},
		{ListStickersParams{Favorites: true}, []string{both.FileHash, older.FileHash}},
		{ListStickersParams{Recent: true}, []string{both.FileHash, recentOnly.FileHash}},
		{ListStickersParams{Limit: 1}, []string{both.FileHash}},
	} {
		got := listOnlySticker(t, db, tt.params)
		var hashes []string
		for _, s := range got {
			hashes = append(hashes, s.FileHash)
		}
		if strings.Join(hashes, ",") != strings.Join(tt.want, ",") {
			t.Fatalf("%+v listed %v, want %v", tt.params, hashes, tt.want)
		}
	}

	got := listOnlySticker(t, db, ListStickersParams{Limit: 1})
	digest := sha256.Sum256([]byte("both"))
	if !bytes.Equal(got[0].Media.FileSHA256, digest[:]) || !got[0].Downloadable {
		t.Fatalf("sticker media = %+v", got[0])
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"media_key", "direct_path", "MediaKey", "DirectPath", base64.StdEncoding.EncodeToString(both.MediaKey)} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("sticker JSON exposes %s: %s", secret, encoded)
		}
	}
}

func TestListStickersToleratesReadOnlyStoreWithoutTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wacli.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if exists, err := db.tableExists("stickers"); err != nil || !exists {
		t.Fatalf("stickers table exists = %v, err = %v", exists, err)
	}
	db.Close()
	raw, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`DROP TABLE stickers`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if got, err := ro.ListStickers(ListStickersParams{}); err != nil || len(got) != 0 {
		t.Fatalf("read-only list on an older store = %v, %v", got, err)
	}
}

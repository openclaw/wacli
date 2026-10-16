package store

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testSticker(name string, upload byte) (string, StickerMedia) {
	digest := sha256.Sum256([]byte(name))
	return base64.StdEncoding.EncodeToString(digest[:]), StickerMedia{
		DirectPath:    "/v/t62/" + name + string('a'+rune(upload)),
		MediaKey:      bytes.Repeat([]byte{upload}, 32),
		FileEncSHA256: bytes.Repeat([]byte{upload + 1}, 32),
		FileLength:    4096,
		MimeType:      "image/webp",
		Width:         512,
		Height:        512,
	}
}

func setFavorite(t *testing.T, db *DB, hash string, media StickerMedia, favorite bool, at time.Time) {
	t.Helper()
	if err := db.SetFavoriteSticker(SetFavoriteStickerParams{FileHash: hash, Media: media, Favorite: favorite, ChangedAt: at}); err != nil {
		t.Fatal(err)
	}
}

func upsertHistory(t *testing.T, db *DB, p UpsertHistoryStickerParams) {
	t.Helper()
	if err := db.UpsertHistorySticker(p); err != nil {
		t.Fatal(err)
	}
}

func removeRecent(t *testing.T, db *DB, hash string, at time.Time) {
	t.Helper()
	if err := db.RemoveRecentSticker(hash, at); err != nil {
		t.Fatal(err)
	}
}

func listStickers(t *testing.T, db *DB, p ListStickersParams) []Sticker {
	t.Helper()
	got, err := db.ListStickers(p)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func listDownloads(t *testing.T, db *DB, p ListStickersParams) []StickerDownloadInfo {
	t.Helper()
	got, err := db.ListStickerDownloads(p)
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
			hash, first := testSticker("cat", 1)
			_, second := testSticker("cat", 2)
			apply := []struct {
				media    StickerMedia
				favorite bool
				at       time.Time
			}{{first, true, older}, {second, false, newer}}
			if order != "in order" {
				apply[0], apply[1] = apply[1], apply[0]
			}
			for _, a := range apply {
				setFavorite(t, db, hash, a.media, a.favorite, a.at)
			}
			if got := listStickers(t, db, ListStickersParams{}); len(got) != 0 {
				t.Fatalf("unfavorited sticker still listed: %+v", got)
			}
			// The newest mutation's media is kept even when an older one
			// arrives later: a media key only decrypts its own upload.
			setFavorite(t, db, hash, first, true, newer.Add(time.Minute))
			got := listDownloads(t, db, ListStickersParams{Favorites: true})
			if len(got) != 1 || got[0].DirectPath != first.DirectPath || !bytes.Equal(got[0].MediaKey, first.MediaKey) {
				t.Fatalf("refavorited sticker = %+v", got)
			}
			setFavorite(t, db, hash, second, true, older)
			got = listDownloads(t, db, ListStickersParams{Favorites: true})
			if len(got) != 1 || got[0].DirectPath != first.DirectPath {
				t.Fatalf("older mutation replaced the media: %+v", got)
			}
			setFavorite(t, db, hash, second, false, newer.Add(time.Minute-time.Millisecond))
			if got := listStickers(t, db, ListStickersParams{Favorites: true}); len(got) != 1 {
				t.Fatalf("an unfavorite one millisecond older removed the favorite: %+v", got)
			}
		})
	}
}

func TestStickerWithoutDigestIsNotDownloadable(t *testing.T) {
	db := openTestDB(t)
	_, media := testSticker("bad-identity", 1)
	setFavorite(t, db, "not-a-sha256", media, true, time.Now())
	got := listStickers(t, db, ListStickersParams{})
	if len(got) != 1 || got[0].Downloadable {
		t.Fatalf("sticker without a SHA-256 file hash = %+v, want listed but not downloadable", got)
	}
	if dl := listDownloads(t, db, ListStickersParams{}); len(dl) != 1 || dl[0].FileSHA256 != nil {
		t.Fatalf("download info = %+v, want no digest", dl)
	}
}

func TestFavoriteStickerKeepsMediaWhenMutationHasNone(t *testing.T) {
	db := openTestDB(t)
	hash, full := testSticker("dog", 1)
	at := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	setFavorite(t, db, hash, full, true, at)
	// Some clients send only the flag; partial media must never land.
	setFavorite(t, db, hash, StickerMedia{DirectPath: "/v/t62/other"}, true, at.Add(time.Minute))
	got := listStickers(t, db, ListStickersParams{})
	if len(got) != 1 || !got[0].Downloadable || got[0].FavoritedAt == nil || !got[0].FavoritedAt.Equal(at.Add(time.Minute)) {
		t.Fatalf("sticker = %+v", got)
	}
	if dl := listDownloads(t, db, ListStickersParams{}); dl[0].DirectPath != full.DirectPath {
		t.Fatalf("partial media replaced the stored media: %+v", dl[0])
	}
}

func TestHistoryFavoriteStickerNeverOverridesAppState(t *testing.T) {
	db := openTestDB(t)
	unfavorited, foxMedia := testSticker("fox", 1)
	setFavorite(t, db, unfavorited, StickerMedia{}, false, time.Now())
	listed, owlMedia := testSticker("owl", 3)
	upsertHistory(t, db, UpsertHistoryStickerParams{FileHash: unfavorited, Media: foxMedia, Favorite: true})
	upsertHistory(t, db, UpsertHistoryStickerParams{FileHash: listed, Media: owlMedia, Favorite: true})
	got := listStickers(t, db, ListStickersParams{})
	if len(got) != 1 || got[0].FileHash != listed || !got[0].Favorite || got[0].FavoritedAt != nil {
		t.Fatalf("stickers = %+v, want only the history favorite", got)
	}
	// History fills missing media but never replaces stored media.
	_, laterOwl := testSticker("owl", 7)
	upsertHistory(t, db, UpsertHistoryStickerParams{FileHash: listed, Media: laterOwl, Favorite: true})
	if dl := listDownloads(t, db, ListStickersParams{}); dl[0].DirectPath != owlMedia.DirectPath {
		t.Fatalf("history replaced stored media: %+v", dl[0])
	}
}

func TestRecentStickersHonorRemovalInEitherOrder(t *testing.T) {
	sent := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	db := openTestDB(t)
	recent := func(name string, weight float32, at time.Time) string {
		hash, media := testSticker(name, 1)
		upsertHistory(t, db, UpsertHistoryStickerParams{FileHash: hash, Media: media, Recent: true, Weight: weight, LastSentAt: at})
		return hash
	}
	removedLater := recent("removed-later", 1, sent)
	removeRecent(t, db, removedLater, sent)
	removedFirst, _ := testSticker("removed-first", 2)
	removeRecent(t, db, removedFirst, sent)
	recent("removed-first", 1, sent)
	sentAgain, _ := testSticker("sent-again", 3)
	removeRecent(t, db, sentAgain, sent)
	recent("sent-again", 2, sent.Add(800*time.Millisecond))
	kept := recent("kept", 3, sent)

	got := listStickers(t, db, ListStickersParams{Recent: true})
	if len(got) != 2 || got[0].FileHash != kept || got[1].FileHash != sentAgain {
		t.Fatalf("recent stickers = %+v, want kept then sent-again by weight", got)
	}
	if got[0].LastSentAt == nil || !got[0].LastSentAt.Equal(sent) || got[0].RecentWeight != 3 || got[0].Favorite {
		t.Fatalf("recent sticker = %+v", got[0])
	}
}

func TestListStickersFiltersAndOrders(t *testing.T) {
	db := openTestDB(t)
	at := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	both, bothMedia := testSticker("both", 1)
	older, olderMedia := testSticker("older-favorite", 2)
	recentOnly, recentMedia := testSticker("recent-only", 3)
	setFavorite(t, db, both, bothMedia, true, at)
	upsertHistory(t, db, UpsertHistoryStickerParams{FileHash: both, Media: bothMedia, Recent: true, Weight: 1, LastSentAt: at})
	setFavorite(t, db, older, olderMedia, true, at.Add(-time.Hour))
	upsertHistory(t, db, UpsertHistoryStickerParams{FileHash: recentOnly, Media: recentMedia, Recent: true, Weight: 1, LastSentAt: at})

	for _, tt := range []struct {
		params ListStickersParams
		want   []string
	}{
		{ListStickersParams{}, []string{both, older, recentOnly}},
		{ListStickersParams{Favorites: true, Recent: true}, []string{both, older, recentOnly}},
		{ListStickersParams{Favorites: true}, []string{both, older}},
		{ListStickersParams{Recent: true}, []string{both, recentOnly}},
		{ListStickersParams{Limit: 1}, []string{both}},
	} {
		var hashes []string
		for _, s := range listStickers(t, db, tt.params) {
			hashes = append(hashes, s.FileHash)
		}
		if strings.Join(hashes, ",") != strings.Join(tt.want, ",") {
			t.Fatalf("%+v listed %v, want %v", tt.params, hashes, tt.want)
		}
	}

	dl := listDownloads(t, db, ListStickersParams{Limit: 1})
	digest := sha256.Sum256([]byte("both"))
	if len(dl) != 1 || dl[0].FileHash != both || !bytes.Equal(dl[0].FileSHA256, digest[:]) || dl[0].FileLength != 4096 {
		t.Fatalf("download info = %+v", dl)
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

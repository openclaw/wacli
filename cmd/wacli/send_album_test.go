package main

import (
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
)

// fakeAlbumWA records every message so tests can check the album header and
// the association of each item; IDs are handed out in send order.
type fakeAlbumWA struct {
	*fakeSendFileWA
	sent []*waProto.Message
	ids  []types.MessageID
}

func (f *fakeAlbumWA) SendProtoMessage(_ context.Context, to types.JID, msg *waProto.Message) (types.MessageID, error) {
	f.wireTarget = to
	id := f.ids[len(f.sent)]
	f.sent = append(f.sent, msg)
	return id, nil
}

func writeAlbumImage(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create image: %v", err)
	}
	defer file.Close()
	if err := png.Encode(file, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatalf("encode image: %v", err)
	}
	return path
}

func TestSendAlbumLinksEveryItemToTheAlbumHeader(t *testing.T) {
	a, db, _ := newSendFileFixture(t)
	fake := &fakeAlbumWA{fakeSendFileWA: &fakeSendFileWA{}, ids: []types.MessageID{"ALBUM", "IMG1", "IMG2", "IMG3"}}
	a.wa = fake
	dir := t.TempDir()
	files := []string{writeAlbumImage(t, dir, "a.png"), writeAlbumImage(t, dir, "b.png"), writeAlbumImage(t, dir, "c.png")}
	to := types.NewJID("15550000001", types.DefaultUserServer)

	res, err := sendAlbum(context.Background(), a, func(context.Context) error { return nil }, to, files, "trip")
	if err != nil {
		t.Fatalf("sendAlbum: %v", err)
	}
	if res.id != "ALBUM" || strings.Join(res.ids, ",") != "IMG1,IMG2,IMG3" || len(res.files) != 3 {
		t.Fatalf("outcome = %+v", res)
	}
	if len(fake.sent) != 4 {
		t.Fatalf("sent %d messages, want header + 3 items", len(fake.sent))
	}
	header := fake.sent[0].GetAlbumMessage()
	if header == nil || header.GetExpectedImageCount() != 3 || header.GetExpectedVideoCount() != 0 {
		t.Fatalf("album header = %+v", fake.sent[0])
	}
	for i, msg := range fake.sent[1:] {
		if msg.GetImageMessage() == nil {
			t.Fatalf("item %d is not an image: %+v", i, msg)
		}
		assoc := msg.GetMessageContextInfo().GetMessageAssociation()
		if assoc.GetAssociationType() != waE2E.MessageAssociation_MEDIA_ALBUM {
			t.Fatalf("item %d association = %v", i, assoc.GetAssociationType())
		}
		key := assoc.GetParentMessageKey()
		if key.GetID() != "ALBUM" || !key.GetFromMe() || key.GetRemoteJID() != to.String() {
			t.Fatalf("item %d parent key = %+v", i, key)
		}
		wantCaption := ""
		if i == 0 {
			wantCaption = "trip"
		}
		if got := msg.GetImageMessage().GetCaption(); got != wantCaption {
			t.Fatalf("item %d caption = %q, want %q", i, got, wantCaption)
		}
	}
	if _, err := db.GetMessage(to.String(), "IMG2"); err != nil {
		t.Fatalf("album item not persisted: %v", err)
	}
}

func TestSendAlbumRejectsBadInputBeforeSending(t *testing.T) {
	dir := t.TempDir()
	img := writeAlbumImage(t, dir, "a.png")
	doc := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(doc, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	tooMany := make([]string, maxAlbumFiles+1)
	for i := range tooMany {
		tooMany[i] = img
	}
	user := types.NewJID("15550000001", types.DefaultUserServer)
	for _, tc := range []struct {
		name  string
		to    types.JID
		files []string
		want  string
	}{
		{name: "one file", to: user, files: []string{img}, want: "at least"},
		{name: "too many", to: user, files: tooMany, want: "at most"},
		{name: "document", to: user, files: []string{img, doc}, want: "images or videos"},
		{name: "missing", to: user, files: []string{img, filepath.Join(dir, "missing.png")}, want: "missing.png"},
		{name: "channel", to: types.NewJID("123", types.NewsletterServer), files: []string{img, img}, want: "channels"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _, _ := newSendFileFixture(t)
			fake := &fakeAlbumWA{fakeSendFileWA: &fakeSendFileWA{}}
			a.wa = fake
			_, err := sendAlbum(context.Background(), a, func(context.Context) error { return nil }, tc.to, tc.files, "")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
			if len(fake.sent) != 0 {
				t.Fatalf("sent %d messages before rejecting", len(fake.sent))
			}
		})
	}
}

func TestSendAlbumCommandRequiresRecipientAndFiles(t *testing.T) {
	flags := &rootFlags{}
	for _, args := range [][]string{{"--file", "a.png", "--file", "b.png"}, {"--to", "15550000001", "--file", "a.png"}} {
		cmd := newSendAlbumCmd(flags)
		cmd.SetArgs(args)
		cmd.SilenceUsage = true
		cmd.SilenceErrors = true
		if err := cmd.Execute(); err == nil {
			t.Fatalf("args %v: expected an error", args)
		}
	}
}

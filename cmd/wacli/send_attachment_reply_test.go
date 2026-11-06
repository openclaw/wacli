package main

import (
	"context"
	"crypto/sha256"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/fsutil"
	"github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// quotingUploadWA records uploads beside the text-send recording transport, so
// attachment replies can be checked against the same identity fixtures as text.
type quotingUploadWA struct {
	*historyTextWA
	uploads int
}

func (w *quotingUploadWA) Upload(_ context.Context, data []byte, _ whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
	w.uploads++
	hash := sha256.Sum256(data)
	return whatsmeow.UploadResponse{FileSHA256: hash[:], FileLength: uint64(len(data))}, nil
}

// sendAttachmentReply sends a document or a sticker through the production
// send path and returns the accepted ID.
func sendAttachmentReply(t *testing.T, kind string, a *fakeSendFileApp, to types.JID, replyTo, replyToSender string) (types.MessageID, error) {
	t.Helper()
	if kind == "sticker" {
		path := filepath.Join(t.TempDir(), "sticker.webp")
		if err := fsutil.WritePrivateFile(path, testWebPVP8X(stickerDimension, stickerDimension, false, nil)); err != nil {
			t.Fatalf("write sticker: %v", err)
		}
		outcome, err := sendSticker(context.Background(), a, to, path, sendStickerOptions{replyTo: replyTo, replyToSender: replyToSender})
		return types.MessageID(outcome.id), err
	}
	path := filepath.Join(t.TempDir(), "note.txt")
	if err := fsutil.WritePrivateFile(path, []byte("hello")); err != nil {
		t.Fatalf("write file: %v", err)
	}
	outcome, err := sendFile(context.Background(), a, to, path, sendFileOptions{replyTo: replyTo, replyToSender: replyToSender})
	return types.MessageID(outcome.id), err
}

func attachmentReplyContext(kind string, msg *waProto.Message) *waProto.ContextInfo {
	if kind == "sticker" {
		return msg.GetStickerMessage().GetContextInfo()
	}
	return msg.GetDocumentMessage().GetContextInfo()
}

func TestSendAttachmentReplyQuotesLikeText(t *testing.T) {
	pn := types.NewJID("15550000001", types.DefaultUserServer)
	lid := types.NewJID("20001", types.HiddenUserServer)
	self := types.NewJID("15550000009", types.DefaultUserServer)
	selfLID := types.NewJID("20009", types.HiddenUserServer)
	group := types.NewJID("12345-123", types.GroupServer)
	for _, kind := range []string{"file", "sticker"} {
		for _, tc := range []struct {
			name           string
			to, storedChat types.JID
			sender         string
			own, unsynced  bool
			override       string
			wantWireSender string
			wantQuotedText string
		}{
			{name: "incoming direct", to: pn, storedChat: pn, sender: pn.String(), wantWireSender: pn.String(), wantQuotedText: "Original"},
			{name: "incoming stored under chat alias", to: lid, storedChat: pn, sender: pn.String(), wantWireSender: pn.String(), wantQuotedText: "Original"},
			{name: "own message in LID chat", to: lid, storedChat: pn, own: true, wantWireSender: selfLID.String(), wantQuotedText: "Original"},
			{name: "unsynced group with explicit sender", to: group, unsynced: true, override: "15550000001", wantWireSender: pn.String()},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				db := openSendTestDB(t)
				sender := &recordingTextSender{linkedJID: self.String(), linkedLID: selfLID}
				if tc.to == lid {
					sender.lidToPN = pn
				}
				transport := &quotingUploadWA{historyTextWA: &historyTextWA{sender: sender}}
				a := &fakeSendFileApp{wa: transport, db: db}
				if !tc.unsynced {
					if err := db.UpsertChat(tc.storedChat.String(), "dm", "Fixture", time.Now()); err != nil {
						t.Fatal(err)
					}
					if err := db.UpsertMessage(store.UpsertMessageParams{ChatJID: tc.storedChat.String(), MsgID: "ORIGINAL", SenderJID: tc.sender, FromMe: tc.own, Timestamp: time.Now(), Text: "Original"}); err != nil {
						t.Fatal(err)
					}
				}

				if _, err := sendAttachmentReply(t, kind, a, tc.to, " ORIGINAL ", tc.override); err != nil {
					t.Fatalf("send: %v", err)
				}
				if transport.uploads != 1 || sender.protoCalls != 1 || sender.protoRecipient != tc.to {
					t.Fatalf("uploads=%d sends=%d recipient=%s", transport.uploads, sender.protoCalls, sender.protoRecipient)
				}
				wire := attachmentReplyContext(kind, sender.protoMsg)
				if wire.GetStanzaID() != "ORIGINAL" || wire.GetParticipant() != tc.wantWireSender {
					t.Fatalf("native quote = %q/%q, want ORIGINAL/%q", wire.GetStanzaID(), wire.GetParticipant(), tc.wantWireSender)
				}
				if got := wire.GetQuotedMessage().GetConversation(); got != tc.wantQuotedText {
					t.Fatalf("quoted content = %q, want %q", got, tc.wantQuotedText)
				}
				if tc.unsynced && wire.GetQuotedMessage() != nil {
					t.Fatalf("quoted message = %v, want nil without stored content", wire.GetQuotedMessage())
				}
			})
		}
	}
}

func TestSendAttachmentReplyRefusesUnquotableTargetBeforeUpload(t *testing.T) {
	chat := types.NewJID("15550000001", types.DefaultUserServer)
	for _, kind := range []string{"file", "sticker"} {
		for _, tc := range []struct {
			name      string
			seed      *store.UpsertMessageParams
			wantError string
		}{
			{name: "unknown message", wantError: "not found in local store"},
			{
				name:      "unsupported stored content",
				seed:      &store.UpsertMessageParams{ChatJID: chat.String(), MsgID: "ORIGINAL", SenderJID: chat.String(), Timestamp: time.Now(), MediaType: "location"},
				wantError: "unsupported stored media type",
			},
			{
				name:      "deleted message",
				seed:      &store.UpsertMessageParams{ChatJID: chat.String(), MsgID: "ORIGINAL", SenderJID: chat.String(), Timestamp: time.Now(), Text: "Original", Revoked: true},
				wantError: "stored message was deleted",
			},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				db := openSendTestDB(t)
				if tc.seed != nil {
					if err := db.UpsertChat(chat.String(), "dm", "Fixture", time.Now()); err != nil {
						t.Fatal(err)
					}
					if err := db.UpsertMessage(*tc.seed); err != nil {
						t.Fatal(err)
					}
				}
				sender := &recordingTextSender{linkedJID: "15550000009@s.whatsapp.net"}
				transport := &quotingUploadWA{historyTextWA: &historyTextWA{sender: sender}}
				a := &fakeSendFileApp{wa: transport, db: db}

				_, err := sendAttachmentReply(t, kind, a, chat, "ORIGINAL", "")
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("error = %v, want %q", err, tc.wantError)
				}
				if transport.uploads != 0 || sender.protoCalls != 0 {
					t.Fatalf("uploads=%d sends=%d, want 0/0", transport.uploads, sender.protoCalls)
				}
			})
		}
	}
}

func TestSendAttachmentReplyPreservesMediaQuotePayload(t *testing.T) {
	chat := types.NewJID("15550000001", types.DefaultUserServer)
	for _, kind := range []string{"file", "sticker"} {
		for _, media := range []string{"image", "video", "audio", "document", "sticker"} {
			t.Run(kind+"/"+media, func(t *testing.T) {
				db := openSendTestDB(t)
				if err := db.UpsertChat(chat.String(), "dm", "Fixture", time.Now()); err != nil {
					t.Fatal(err)
				}
				if err := db.UpsertMessage(store.UpsertMessageParams{
					ChatJID: chat.String(), MsgID: "MEDIA", SenderJID: chat.String(), Timestamp: time.Now(),
					MediaType: media, MediaCaption: "Original caption", Filename: "original.bin", MimeType: "application/octet-stream",
					DirectPath: "/fixture/media", MediaKey: []byte("fixture-key"), FileSHA256: []byte("fixture-sha"),
					FileEncSHA256: []byte("fixture-encrypted-sha"), FileLength: 123,
				}); err != nil {
					t.Fatal(err)
				}
				sender := &recordingTextSender{linkedJID: "15550000009@s.whatsapp.net"}
				transport := &quotingUploadWA{historyTextWA: &historyTextWA{sender: sender}}
				if _, err := sendAttachmentReply(t, kind, &fakeSendFileApp{wa: transport, db: db}, chat, "MEDIA", ""); err != nil {
					t.Fatal(err)
				}
				quote := attachmentReplyContext(kind, sender.protoMsg)
				if quote.GetStanzaID() != "MEDIA" || quote.GetParticipant() != chat.String() {
					t.Fatalf("quote identity = %v", quote)
				}
				// Round-trip the actual wire payload through the production media parser.
				parsed := wa.ParseHistoryMessage(chat.String(), &waProto.WebMessageInfo{Key: &waProto.MessageKey{ID: proto.String("MEDIA"), RemoteJID: proto.String(chat.String())}, Message: quote.GetQuotedMessage()}).Media
				if parsed == nil || parsed.Type != media || parsed.DirectPath != "/fixture/media" || parsed.FileLength != 123 {
					t.Fatalf("media quote = %+v", parsed)
				}
				if string(parsed.MediaKey) != "fixture-key" || string(parsed.FileSHA256) != "fixture-sha" || string(parsed.FileEncSHA256) != "fixture-encrypted-sha" {
					t.Fatal("quote lost attachment retrieval metadata")
				}
			})
		}
	}
}

func TestSendAttachmentReplySenderDoesNotBypassMissingDirectQuote(t *testing.T) {
	chat := types.NewJID("15550000001", types.DefaultUserServer)
	for _, kind := range []string{"file", "sticker"} {
		t.Run(kind, func(t *testing.T) {
			sender := &recordingTextSender{linkedJID: "15550000009@s.whatsapp.net"}
			transport := &quotingUploadWA{historyTextWA: &historyTextWA{sender: sender}}
			_, err := sendAttachmentReply(t, kind, &fakeSendFileApp{wa: transport, db: openSendTestDB(t)}, chat, "MISSING", chat.String())
			if err == nil || !strings.Contains(err.Error(), "not found in local store") || transport.uploads != 0 || sender.protoCalls != 0 {
				t.Fatalf("err=%v uploads=%d sends=%d", err, transport.uploads, sender.protoCalls)
			}
		})
	}
}

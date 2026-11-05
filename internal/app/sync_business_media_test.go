package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestSyncBusinessHeaderMediaStorageAndQueue(t *testing.T) {
	for _, source := range []string{"live", "history"} {
		t.Run(source, func(t *testing.T) {
			a := newTestApp(t)
			a.wa = newFakeWA()
			chat := types.NewJID("15550000001", types.DefaultUserServer)
			ts := time.Unix(1700000000, 0)
			if err := a.db.UpsertChat(chat.String(), "dm", "Synthetic business", ts); err != nil {
				t.Fatal(err)
			}
			// Re-ingestion must also repair rows stored by the old text-only parser.
			if err := a.db.UpsertMessage(store.UpsertMessageParams{ChatJID: chat.String(), MsgID: "invoice", Text: "Invoice ready", Timestamp: ts}); err != nil {
				t.Fatal(err)
			}
			message := &waProto.Message{TemplateMessage: &waProto.TemplateMessage{HydratedTemplate: &waProto.TemplateMessage_HydratedFourRowTemplate{
				HydratedContentText: proto.String("Invoice ready"),
				Title: &waProto.TemplateMessage_HydratedFourRowTemplate_DocumentMessage{DocumentMessage: &waProto.DocumentMessage{
					FileName: proto.String("invoice.pdf"), Mimetype: proto.String("application/pdf"),
					DirectPath: proto.String("/synthetic/invoice"), MediaKey: []byte{1, 2, 3},
					FileSHA256: []byte{4, 5, 6}, FileEncSHA256: []byte{7, 8, 9}, FileLength: proto.Uint64(123),
				}},
			}}}
			var stored, lastEvent atomic.Int64
			var queued []string
			enqueue := func(jid, id string) { queued = append(queued, jid+"/"+id) }
			if source == "live" {
				a.handleLiveSyncMessage(context.Background(), SyncOptions{DownloadMedia: true}, &events.Message{
					Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: chat}, ID: "invoice", Timestamp: ts}, Message: message,
				}, &stored, enqueue, nil)
			} else {
				a.handleHistorySync(context.Background(), SyncOptions{DownloadMedia: true}, historySyncOf(chat, &waProto.WebMessageInfo{
					Key: &waProto.MessageKey{RemoteJID: proto.String(chat.String()), ID: proto.String("invoice")}, MessageTimestamp: proto.Uint64(uint64(ts.Unix())), Message: message,
				}), &stored, &lastEvent, enqueue)
			}
			row, err := a.db.GetMessage(chat.String(), "invoice")
			if err != nil {
				t.Fatal(err)
			}
			if row.Text != "Invoice ready" || row.MediaType != "document" || row.Filename != "invoice.pdf" {
				t.Fatalf("text-only row was not repaired: %+v", row)
			}
			info, err := a.db.GetMediaDownloadInfo(chat.String(), "invoice")
			if err != nil || info.DirectPath != "/synthetic/invoice" || len(info.MediaKey) != 3 {
				t.Fatalf("attachment not retrievable: %+v, %v", info, err)
			}
			if len(queued) != 1 || queued[0] != chat.String()+"/invoice" {
				t.Fatalf("download queue = %v", queued)
			}
		})
	}
}

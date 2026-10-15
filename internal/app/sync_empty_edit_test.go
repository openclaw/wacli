package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestLiveSyncKeepsEmptyEditDistinctFromProtocolReplay(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	chat := types.JID{User: "456", Server: types.GroupServer}
	info := types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: types.JID{User: "123", Server: types.DefaultUserServer}, IsGroup: true}, ID: "original", Timestamp: time.Unix(1700000000, 0)}
	var stored atomic.Int64
	a.handleLiveSyncMessage(context.Background(), SyncOptions{}, &events.Message{Info: info, Message: &waE2E.Message{Conversation: proto.String("before")}}, &stored, nil, nil)
	info.ID = "edit-event"
	info.Timestamp = info.Timestamp.Add(time.Second)
	edit := &events.Message{Info: info, Message: &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(), Key: &waCommon.MessageKey{ID: proto.String("original"), RemoteJID: proto.String(chat.String())}, EditedMessage: &waE2E.Message{Conversation: proto.String("")}}}}
	if pm := wa.ParseLiveMessage(edit); !pm.Edited || pm.HasContent() {
		t.Fatalf("unexpected fixture: %+v", pm)
	}
	captureStderr(t, func() { a.handleLiveSyncMessage(context.Background(), SyncOptions{}, edit, &stored, nil, nil) })
	m, err := a.db.GetMessage(chat.String(), "original")
	if err != nil || !m.Edited || m.Text != "" {
		t.Fatalf("empty edit was discarded as protocol replay: %+v, %v", m, err)
	}
}

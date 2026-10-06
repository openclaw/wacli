package wa

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waWeb"
	waStore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

var errSyntheticPrimaryBoundary = errors.New("synthetic primary-request boundary")

type primaryBoundaryLIDs struct {
	waStore.LIDStore
	own types.JID
}

func (s *primaryBoundaryLIDs) GetLIDForPN(ctx context.Context, pn types.JID) (types.JID, error) {
	if pn.ToNonAD() == s.own {
		return types.EmptyJID, errSyntheticPrimaryBoundary
	}
	return s.LIDStore.GetLIDForPN(ctx, pn)
}

type phoneRequestLog struct {
	waLog.Logger
	attempts, cancelled, decryptErrors chan string
}

func (l *phoneRequestLog) Warnf(format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	var target chan string
	if strings.HasPrefix(format, "Failed to send request for unavailable message") {
		target = l.attempts
	}
	if strings.HasPrefix(format, "Error decrypting message") {
		target = l.decryptErrors
	}
	if target != nil {
		select {
		case target <- message:
		default:
		}
	}
}
func (l *phoneRequestLog) Debugf(format string, args ...any) {
	if strings.HasPrefix(format, "Cancelled delayed request") {
		select {
		case l.cancelled <- fmt.Sprintf(format, args...):
		default:
		}
	}
}

func newPhoneRequestClient(t *testing.T) (*Client, *phoneRequestLog, <-chan *events.UndecryptableMessage) {
	t.Helper()
	c, err := New(Options{StorePath: newPairedSessionStore(t), KeyStateStore: newTestKeyStateStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	c.client.BackgroundEventCtx = ctx
	c.client.SynchronousAck = false
	c.client.Store.LIDs = &primaryBoundaryLIDs{LIDStore: c.client.Store.LIDs, own: c.client.Store.ID.ToNonAD()}
	log := &phoneRequestLog{Logger: waLog.Noop, attempts: make(chan string, 8), cancelled: make(chan string, 8), decryptErrors: make(chan string, 8)}
	c.client.Log = log
	failed := make(chan *events.UndecryptableMessage, 8)
	c.AddEventHandler(func(evt any) {
		if v, ok := evt.(*events.UndecryptableMessage); ok {
			failed <- v
		}
	})
	t.Cleanup(func() { cancel(); c.Close() })
	return c, log, failed
}

func usePhoneRequestDelay(t *testing.T, d time.Duration) {
	t.Helper()
	previous := whatsmeow.RequestFromPhoneDelay
	whatsmeow.RequestFromPhoneDelay = d
	t.Cleanup(func() { whatsmeow.RequestFromPhoneDelay = previous })
}

func failedCipherNode(id string, group bool) *waBinary.Node {
	sender := types.NewJID("15550000001", types.DefaultUserServer)
	node := &waBinary.Node{Tag: "message", Attrs: waBinary.Attrs{"id": id, "from": sender, "t": fmt.Sprint(time.Now().Unix()), "type": "text"}}
	cipher := []byte{1, 2, 3}
	kind := "msg"
	if group {
		node.Attrs["from"] = types.NewJID("120363000000001", types.GroupServer)
		node.Attrs["participant"] = sender
		// Valid sender-key envelope, with no receiving sender key in this store.
		cipher = append([]byte{0x33, 0x08, 0x01, 0x10, 0x01, 0x1a, 0x01, 0x00}, make([]byte, 64)...)
		kind = "skmsg"
	}
	node.Content = []waBinary.Node{{Tag: "enc", Attrs: waBinary.Attrs{"v": "2", "type": kind}, Content: cipher}}
	return node
}

func requirePrimaryAttempt(t *testing.T, log *phoneRequestLog) {
	t.Helper()
	select {
	case message := <-log.attempts:
		if !strings.Contains(message, errSyntheticPrimaryBoundary.Error()) {
			t.Fatalf("unexpected send boundary: %s", message)
		}
	case <-time.After(time.Second):
		t.Fatal("failed decryption did not request a primary copy")
	}
}

func TestSDKFailedDecryptionRequestsPrimary(t *testing.T) {
	for _, group := range []bool{false, true} {
		t.Run(fmt.Sprint(group), func(t *testing.T) {
			usePhoneRequestDelay(t, 20*time.Millisecond)
			c, log, failed := newPhoneRequestClient(t)
			c.client.DangerousInternals().HandleEncryptedMessage(t.Context(), failedCipherNode("synthetic-failure", group))
			select {
			case evt := <-failed:
				if evt.IsUnavailable != group {
					select {
					case msg := <-log.decryptErrors:
						t.Log(msg)
					default:
					}
					t.Fatalf("is_unavailable=%v, group=%v", evt.IsUnavailable, group)
				}
			case <-time.After(time.Second):
				t.Fatal("real decryption handler did not report failure")
			}
			requirePrimaryAttempt(t, log)
		})
	}
}

func TestSDKDisabledAndRepeatedFailuresDoNotRequestAnotherCopy(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(fmt.Sprint(disabled), func(t *testing.T) {
			usePhoneRequestDelay(t, 20*time.Millisecond)
			c, log, failed := newPhoneRequestClient(t)
			if disabled {
				c.client.AutomaticMessageRerequestFromPhone = false
			}
			node := failedCipherNode("synthetic-retry", false)
			c.client.DangerousInternals().HandleEncryptedMessage(t.Context(), node)
			<-failed
			if !disabled {
				requirePrimaryAttempt(t, log)
				c.client.DangerousInternals().HandleEncryptedMessage(t.Context(), node)
				<-failed
			}
			select {
			case msg := <-log.attempts:
				t.Fatalf("unexpected primary request: %s", msg)
			case <-time.After(100 * time.Millisecond):
			}
		})
	}
}

func TestSDKPrimaryRerequestCanBeCancelled(t *testing.T) {
	usePhoneRequestDelay(t, time.Hour)
	c, log, failed := newPhoneRequestClient(t)
	c.client.DangerousInternals().HandleEncryptedMessage(t.Context(), failedCipherNode("synthetic-cancel", false))
	<-failed
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		c.client.DangerousInternals().CancelDelayedRequestFromPhone("synthetic-cancel")
		select {
		case <-log.cancelled:
			select {
			case msg := <-log.attempts:
				t.Fatalf("cancelled request sent: %s", msg)
			default:
			}
			return
		case <-deadline.C:
			t.Fatal("delayed request was not cancelled")
		case <-ticker.C:
		}
	}
}

func TestSDKTypedUnavailableStillAttemptsPrimary(t *testing.T) {
	c, log, failed := newPhoneRequestClient(t)
	c.client.AutomaticMessageRerequestFromPhone = false
	node := failedCipherNode("synthetic-view-once", false)
	node.Content = []waBinary.Node{{Tag: "unavailable", Attrs: waBinary.Attrs{"type": "view_once"}}}
	c.client.DangerousInternals().HandleEncryptedMessage(t.Context(), node)
	evt := <-failed
	if evt.UnavailableType != events.UnavailableTypeViewOnce {
		t.Fatalf("type=%q", evt.UnavailableType)
	}
	requirePrimaryAttempt(t, log)
}

func TestSDKPrimaryResponseDeliversNormalMessage(t *testing.T) {
	c, _, _ := newPhoneRequestClient(t)
	chat := types.NewJID("15550000001", types.DefaultUserServer)
	raw, err := proto.Marshal(&waWeb.WebMessageInfo{Key: &waCommon.MessageKey{RemoteJID: proto.String(chat.String()), ID: proto.String("recovered"), FromMe: proto.Bool(false)}, MessageTimestamp: proto.Uint64(uint64(time.Now().Unix())), Message: &waE2E.Message{Conversation: proto.String("synthetic recovered text")}})
	if err != nil {
		t.Fatal(err)
	}
	var received []*events.Message
	c.AddEventHandler(func(evt any) {
		if v, ok := evt.(*events.Message); ok {
			received = append(received, v)
		}
	})
	msg := &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{PeerDataOperationRequestResponseMessage: &waE2E.PeerDataOperationRequestResponseMessage{
		StanzaID: proto.String("synthetic-request"), PeerDataOperationRequestType: waE2E.PeerDataOperationRequestType_PLACEHOLDER_MESSAGE_RESEND.Enum(),
		PeerDataOperationResult: []*waE2E.PeerDataOperationRequestResponseMessage_PeerDataOperationResult{{PlaceholderMessageResendResponse: &waE2E.PeerDataOperationRequestResponseMessage_PeerDataOperationResult_PlaceholderMessageResendResponse{WebMessageInfoBytes: raw}}},
	}}}
	info := &types.MessageInfo{MessageSource: types.MessageSource{Sender: c.client.Store.ID.ToNonAD(), IsFromMe: false}}
	c.client.DangerousInternals().HandleProtocolMessage(t.Context(), info, msg)
	info.IsFromMe = true
	info.Sender.Device = 1
	c.client.DangerousInternals().HandleProtocolMessage(t.Context(), info, msg)
	if len(received) != 0 {
		t.Fatal("non-primary response delivered a recovered message")
	}
	info.Sender.Device = 0
	c.client.DangerousInternals().HandleProtocolMessage(t.Context(), info, msg)
	if len(received) != 1 || received[0].UnavailableRequestID != "synthetic-request" || received[0].Message.GetConversation() != "synthetic recovered text" {
		t.Fatalf("recovered events=%+v", received)
	}
}

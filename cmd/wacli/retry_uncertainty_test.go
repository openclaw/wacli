package main

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
)

// retryReviewSender models the network boundary, not a real WhatsApp account.
// In the post-dispatch case the frame has left before confirmation is lost.
// The concrete library reconnect/socket-loss branch can return ErrNotConnected
// at that point. Every subsequent call represents a fresh SendMessage request.
type retryReviewSender struct {
	recordingTextSender
	firstErr       error
	beforeDispatch bool
	calls          int
	frames         []types.MessageID
}

func (s *retryReviewSender) send() (types.MessageID, error) {
	s.calls++
	if s.calls == 1 && s.beforeDispatch {
		return "", s.firstErr
	}
	id := types.MessageID(fmt.Sprintf("synthetic-message-%d", s.calls))
	s.frames = append(s.frames, id)
	if s.calls == 1 && s.firstErr != nil {
		return "", s.firstErr
	}
	return id, nil
}

func (s *retryReviewSender) SendText(context.Context, types.JID, string) (types.MessageID, error) {
	return s.send()
}

func (s *retryReviewSender) SendProtoMessage(context.Context, types.JID, *waProto.Message) (types.MessageID, error) {
	return s.send()
}

// runRetryReview uses the actual production outer retry and text construction
// paths. Only the connection/sender boundary is fake; all data is synthetic.
func runRetryReview(t *testing.T, sender *retryReviewSender) (types.MessageID, error, int) {
	t.Helper()
	db := openSendTestDB(t)
	reconnects := 0
	id, err := runSendOperation(context.Background(), func(context.Context) error {
		reconnects++
		return nil
	}, func(ctx context.Context) (types.MessageID, error) {
		return sendTextMessageWithSender(ctx, sender, db,
			types.JID{User: "15551110000", Server: types.DefaultUserServer},
			"synthetic retry boundary probe", "", "", nil, nil,
			textEphemeralOptions{}, textSendOptions{allowSelf: true})
	})
	return id, err, reconnects
}

func TestSendRetryUncertaintyControls(t *testing.T) {
	// Controls distinguish a safe preparation retry from lost acknowledgement.
	// A prior frame plus a terminal outcome must not create another frame.
	cases := []struct {
		name                      string
		err                       error
		before                    bool
		calls, frames, reconnects int
		wantErr                   bool
	}{
		{"success", nil, false, 1, 1, 0, false},
		{"lookup_timeout_before_dispatch", whatsmeow.ErrIQTimedOut, true, 2, 1, 1, false},
		// The same unqualified error can occur before or after dispatch, so
		// conservatively leave either outcome to the caller without a new send.
		{"disconnected_before_dispatch", whatsmeow.ErrNotConnected, true, 1, 0, 0, true},
		{"wrapped_disconnected_after_dispatch", fmt.Errorf("message response: %w", whatsmeow.ErrNotConnected), false, 1, 1, 0, true},
		{"message_ack_timeout", whatsmeow.ErrMessageTimedOut, false, 1, 1, 0, true},
		{"wrapped_message_ack_timeout", fmt.Errorf("message response: %w", whatsmeow.ErrMessageTimedOut), false, 1, 1, 0, true},
		{"cancellation_after_dispatch", context.Canceled, false, 1, 1, 0, true},
		{"deadline_after_dispatch", context.DeadlineExceeded, false, 1, 1, 0, true},
		{"server_rejection", errors.New("server returned error 500"), false, 1, 1, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &retryReviewSender{firstErr: tc.err, beforeDispatch: tc.before}
			_, err, reconnects := runRetryReview(t, s)
			if (err != nil) != tc.wantErr || s.calls != tc.calls || len(s.frames) != tc.frames || reconnects != tc.reconnects {
				t.Fatalf("err=%v calls=%d frames=%v reconnects=%d", err, s.calls, s.frames, reconnects)
			}
			t.Logf("calls=%d frames=%v reconnects=%d error=%v", s.calls, s.frames, reconnects, err)
		})
	}
}

func TestSendRetryPostDispatchDisconnectDoesNotStartFreshSend(t *testing.T) {
	// This regression states the required boundary:
	// no fresh message after an error compatible with prior transmission.
	// It does not claim that a live recipient received two messages.
	s := &retryReviewSender{firstErr: whatsmeow.ErrNotConnected}
	id, err, reconnects := runRetryReview(t, s)
	if len(s.frames) != 1 || s.calls != 1 || reconnects != 0 || err == nil || id != "" {
		t.Fatalf("post-dispatch uncertainty started a fresh send: frames=%v calls=%d reconnects=%d result_id=%s error=%v", s.frames, s.calls, reconnects, id, err)
	}
}

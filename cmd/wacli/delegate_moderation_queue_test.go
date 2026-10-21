package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/delegate"
)

func TestModerationDelegatesQueuedBehindSendHonorCallerDeadline(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	for _, tt := range []struct {
		name    string
		spacing sendSpacing
		req     sendDelegateRequest
	}{
		{name: "delete unpaced", req: sendDelegateRequest{Kind: deleteDelegateKind, To: "123@g.us", ID: "ABC"}},
		{name: "delete paced", spacing: sendSpacing{min: time.Millisecond, max: time.Millisecond}, req: sendDelegateRequest{Kind: deleteDelegateKind, To: "123@g.us", ID: "ABC"}},
		{name: "participants unpaced", req: sendDelegateRequest{Kind: participantsDelegateKind, To: "123@g.us", ParticipantAction: "remove", Users: []string{"15550000001"}}},
		{name: "participants paced", spacing: sendSpacing{min: time.Millisecond, max: time.Millisecond}, req: sendDelegateRequest{Kind: participantsDelegateKind, To: "123@g.us", ParticipantAction: "remove", Users: []string{"15550000001"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			storeDir := shortPresenceDelegateStoreDir(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			started := make(chan struct{})
			release := make(chan struct{})
			lookedUp := make(chan struct{}, 1)
			stop, err := startSendDelegateServerForStore(ctx, storeDir, tt.spacing, func(ctx context.Context, req sendDelegateRequest) (sendDelegateResponse, error) {
				switch req.Kind {
				case "text":
					close(started)
					select {
					case <-release:
						return sendDelegateResponse{OK: true}, nil
					case <-ctx.Done():
						return sendDelegateResponse{}, ctx.Err()
					}
				case deleteDelegateKind, participantsDelegateKind:
					lookedUp <- struct{}{}
					return sendDelegateResponse{OK: true}, nil
				default:
					return sendDelegateResponse{}, fmt.Errorf("unexpected kind %q", req.Kind)
				}
			})
			if err != nil {
				t.Fatalf("start delegate server: %v", err)
			}
			defer stop()
			defer close(release)

			// Hold the serialized slot with a send while the request's shorter
			// absolute caller deadline expires. Its relative timeout is longer.
			sendDone := make(chan error, 1)
			go func() {
				_, err := delegateSend(ctx, &rootFlags{storeDir: storeDir, timeout: 3 * time.Second}, sendDelegateRequest{Kind: "text"})
				sendDone <- err
			}()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("send did not acquire the slot")
			}

			conn, err := delegate.Dial(ctx, storeDir)
			if err != nil {
				t.Fatalf("dial delegate socket: %v", err)
			}
			defer conn.Close()
			if err := conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatalf("set client deadline: %v", err)
			}
			req := tt.req
			req.Version = sendDelegateVersion
			req.TimeoutMS = durationMillis(5 * time.Second)
			req.DeadlineUnixMS = time.Now().Add(150 * time.Millisecond).UnixMilli()
			if err := json.NewEncoder(conn).Encode(req); err != nil {
				t.Fatalf("encode request: %v", err)
			}
			var resp sendDelegateResponse
			if err := json.NewDecoder(conn).Decode(&resp); err != nil {
				t.Fatalf("queued request did not receive an explicit refusal: %v", err)
			}
			if resp.OK || !strings.Contains(resp.Error, "it was not sent") {
				t.Fatalf("response = %+v, want refusal before dispatch", resp)
			}

			// Let the send finish, then ensure the expired request cannot run late.
			release <- struct{}{}
			if err := <-sendDone; err != nil {
				t.Fatalf("blocking send: %v", err)
			}
			select {
			case <-lookedUp:
				t.Fatal("expired moderation request was dispatched")
			case <-time.After(50 * time.Millisecond):
			}
		})
	}
}

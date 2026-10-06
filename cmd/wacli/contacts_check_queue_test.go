package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

func TestContactsCheckQueuedBehindSendHonorsCallerDeadline(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	for _, tt := range []struct {
		name    string
		spacing sendSpacing
	}{
		{name: "unpaced"},
		{name: "paced", spacing: sendSpacing{min: time.Millisecond, max: time.Millisecond}},
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
				case contactsCheckKind:
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

			// Hold the serialized slot with a send while the lookup's shorter
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

			conn, err := net.Dial("unix", sendDelegateSocketPath(storeDir))
			if err != nil {
				t.Fatalf("dial delegate socket: %v", err)
			}
			defer conn.Close()
			if err := conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatalf("set client deadline: %v", err)
			}
			req := sendDelegateRequest{
				Version:        sendDelegateVersion,
				Kind:           contactsCheckKind,
				Phones:         []string{"+15550000001"},
				TimeoutMS:      durationMillis(5 * time.Second),
				DeadlineUnixMS: time.Now().Add(150 * time.Millisecond).UnixMilli(),
			}
			if err := json.NewEncoder(conn).Encode(req); err != nil {
				t.Fatalf("encode lookup: %v", err)
			}
			var resp sendDelegateResponse
			if err := json.NewDecoder(conn).Decode(&resp); err != nil {
				t.Fatalf("queued lookup did not receive an explicit refusal: %v", err)
			}
			if resp.OK || !strings.Contains(resp.Error, "it was not sent") {
				t.Fatalf("response = %+v, want refusal before dispatch", resp)
			}

			// Let the send finish, then ensure the expired lookup cannot run late.
			release <- struct{}{}
			if err := <-sendDone; err != nil {
				t.Fatalf("blocking send: %v", err)
			}
			select {
			case <-lookedUp:
				t.Fatal("expired contacts check was dispatched")
			case <-time.After(50 * time.Millisecond):
			}
		})
	}
}

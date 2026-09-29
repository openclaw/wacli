package main

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Regressions for #446: without --send-spacing, a delegated operation still
// queued behind an earlier one when its caller gives up must be refused, never
// dispatched late.

func unexpectedExecute(t *testing.T) sendDelegateExecutor {
	return func(context.Context, sendDelegateRequest) (sendDelegateResponse, error) {
		t.Error("operation executed after its caller's deadline")
		return sendDelegateResponse{OK: true, Sent: true}, nil
	}
}

func roundTripDelegate(t *testing.T, conn net.Conn, req sendDelegateRequest) sendDelegateResponse {
	t.Helper()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set client deadline: %v", err)
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		t.Fatalf("encode request: %v", err)
	}
	var resp sendDelegateResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp
}

func TestUnpacedQueueWaitHonorsRequestTimeout(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()

	sendSlot := make(chan struct{}, 1) // empty: an earlier send owns it
	done := make(chan struct{})
	go func() {
		defer close(done)
		handleSendDelegateConn(context.Background(), serverConn, unexpectedExecute(t), sendSlot, newSendPacer(sendSpacing{}))
	}()

	resp := roundTripDelegate(t, clientConn, sendDelegateRequest{
		Version:   sendDelegateVersion,
		Kind:      "text",
		TimeoutMS: 20,
	})
	<-done
	if resp.OK || !strings.Contains(resp.Error, "it was not sent") {
		t.Fatalf("response = %+v, want an explicit not-sent refusal", resp)
	}
}

func TestUnpacedRefusesExpiredCallerDeadlineWithFreeSlot(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()

	sendSlot := make(chan struct{}, 1)
	sendSlot <- struct{}{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		handleSendDelegateConn(context.Background(), serverConn, unexpectedExecute(t), sendSlot, newSendPacer(sendSpacing{}))
	}()

	resp := roundTripDelegate(t, clientConn, sendDelegateRequest{
		Version:        sendDelegateVersion,
		Kind:           "text",
		TimeoutMS:      durationMillis(10 * time.Minute),
		DeadlineUnixMS: time.Now().Add(-time.Second).UnixMilli(),
	})
	<-done
	if resp.OK || !strings.Contains(resp.Error, "it was not sent") {
		t.Fatalf("response = %+v, want an expired caller deadline to block dispatch", resp)
	}
	if len(sendSlot) != 1 {
		t.Fatal("refused request did not return the send slot")
	}
}

func TestUnpacedReplyMarginLeavesRoomForRefusal(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()

	sendSlot := make(chan struct{}, 1) // held by an earlier send
	done := make(chan struct{})
	go func() {
		defer close(done)
		handleSendDelegateConn(context.Background(), serverConn, unexpectedExecute(t), sendSlot, newSendPacer(sendSpacing{}))
	}()

	// The caller stops reading at its deadline. The refusal must arrive first.
	callerDeadline := time.Now().Add(sendDelegateReplyMargin + 200*time.Millisecond)
	if err := clientConn.SetDeadline(callerDeadline); err != nil {
		t.Fatalf("set client deadline: %v", err)
	}
	req := sendDelegateRequest{
		Version:        sendDelegateVersion,
		Kind:           "text",
		TimeoutMS:      durationMillis(10 * time.Minute),
		DeadlineUnixMS: callerDeadline.UnixMilli(),
	}
	if err := json.NewEncoder(clientConn).Encode(req); err != nil {
		t.Fatalf("encode request: %v", err)
	}
	var resp sendDelegateResponse
	if err := json.NewDecoder(clientConn).Decode(&resp); err != nil {
		t.Fatalf("refusal did not arrive before the caller's deadline: %v", err)
	}
	<-done
	if resp.OK || !strings.Contains(resp.Error, "it was not sent") {
		t.Fatalf("response = %+v, want an explicit not-sent refusal", resp)
	}
}

// The #446 scenario end to end: a send queued behind a slow operation times out
// on the client and must not reach the wire once the slow operation finishes.
func TestProductionServerDropsQueuedSendAfterCallerTimeout(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	storeDir := shortPresenceDelegateStoreDir(t)

	release := make(chan struct{})
	started := make(chan struct{}, 1)
	var executed atomic.Int32
	stop, err := startSendDelegateServerForStore(context.Background(), storeDir, sendSpacing{}, func(ctx context.Context, req sendDelegateRequest) (sendDelegateResponse, error) {
		executed.Add(1)
		if req.Message == "slow" {
			started <- struct{}{}
			<-release
		}
		return sendDelegateResponse{OK: true, Sent: true, ID: req.Message}, nil
	})
	if err != nil {
		t.Fatalf("start delegate server: %v", err)
	}
	defer stop()

	slowFlags := &rootFlags{storeDir: storeDir, timeout: 10 * time.Second}
	slowDone := make(chan error, 1)
	go func() {
		_, err := delegateSend(context.Background(), slowFlags, sendDelegateRequest{Kind: "text", Message: "slow"})
		slowDone <- err
	}()
	<-started

	queuedFlags := &rootFlags{storeDir: storeDir, timeout: sendDelegateReplyMargin + 300*time.Millisecond}
	_, err = delegateSend(context.Background(), queuedFlags, sendDelegateRequest{Kind: "text", Message: "queued"})
	if err == nil || !strings.Contains(err.Error(), "it was not sent") {
		t.Fatalf("queued send error = %v, want an explicit not-sent refusal", err)
	}

	close(release)
	if err := <-slowDone; err != nil {
		t.Fatalf("slow send: %v", err)
	}
	time.Sleep(200 * time.Millisecond) // give a late dispatch the chance to happen
	if got := executed.Load(); got != 1 {
		t.Fatalf("executed %d operations, want only the slow one", got)
	}
}

func TestDelegateSendTimeoutWarnsAgainstBlindRetry(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	storeDir := shortPresenceDelegateStoreDir(t)
	ln, err := net.Listen("unix", sendDelegateSocketPath(storeDir))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var req sendDelegateRequest
		_ = json.NewDecoder(conn).Decode(&req)
		time.Sleep(2 * time.Second) // accepted, never answers in time
	}()

	flags := &rootFlags{storeDir: storeDir, timeout: 200 * time.Millisecond}
	_, err = delegateSend(context.Background(), flags, sendDelegateRequest{Kind: "text", Message: "hi"})
	if err == nil || !strings.Contains(err.Error(), "may still have gone through") {
		t.Fatalf("error = %v, want a warning that the send may have gone through", err)
	}
}

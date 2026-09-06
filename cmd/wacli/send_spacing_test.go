package main

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

type deadlineRecordingConn struct {
	net.Conn
	deadlines []time.Time
}

func executeDelegatedSendWithoutApp(ctx context.Context, req sendDelegateRequest) (sendDelegateResponse, error) {
	return executeDelegatedSend(ctx, nil, req)
}

func (c *deadlineRecordingConn) SetDeadline(deadline time.Time) error {
	c.deadlines = append(c.deadlines, deadline)
	return c.Conn.SetDeadline(deadline)
}

func TestParseSendSpacing(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantMin time.Duration
		wantMax time.Duration
		wantErr bool
	}{
		{name: "empty disables", raw: "", wantMin: 0, wantMax: 0},
		{name: "fixed gap", raw: "2s", wantMin: 2 * time.Second, wantMax: 2 * time.Second},
		{name: "range", raw: "500ms-5s", wantMin: 500 * time.Millisecond, wantMax: 5 * time.Second},
		{name: "range with spaces", raw: "  1s - 2s ", wantMin: time.Second, wantMax: 2 * time.Second},
		{name: "equal range", raw: "3s-3s", wantMin: 3 * time.Second, wantMax: 3 * time.Second},
		{name: "max below min", raw: "5s-500ms", wantErr: true},
		{name: "unparseable", raw: "soon", wantErr: true},
		{name: "unparseable max", raw: "1s-later", wantErr: true},
		{name: "negative", raw: "-1s", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseSendSpacing(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseSendSpacing(%q) = %+v, want error", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSendSpacing(%q) unexpected error: %v", tc.raw, err)
			}
			if got.min != tc.wantMin || got.max != tc.wantMax {
				t.Fatalf("parseSendSpacing(%q) = {min:%s max:%s}, want {min:%s max:%s}", tc.raw, got.min, got.max, tc.wantMin, tc.wantMax)
			}
			if got.enabled() != (tc.wantMax > 0) {
				t.Fatalf("enabled() = %v, want %v", got.enabled(), tc.wantMax > 0)
			}
		})
	}
}

// fakePacer builds a pacer with a controllable clock: sleeping advances the
// clock, so successive sends observe elapsed time deterministically.
func fakePacer(spacing sendSpacing, rng func(int64) int64) (*sendPacer, *[]time.Duration) {
	clock := time.Unix(1000, 0)
	slept := []time.Duration{}
	p := &sendPacer{
		spacing: spacing,
		now:     func() time.Time { return clock },
		sleep: func(_ context.Context, d time.Duration) {
			slept = append(slept, d)
			clock = clock.Add(d)
		},
		rng: rng,
	}
	return p, &slept
}

// paceOnce models a zero-duration delegated send: wait within ctx, then record
// completion if the send would be dispatched.
func paceOnce(p *sendPacer, ctx context.Context) bool {
	if !p.enabled() {
		return true
	}
	if !p.wait(ctx) {
		return false
	}
	p.record()
	return true
}

func TestSendPacerDisabledNeverSleeps(t *testing.T) {
	p, slept := fakePacer(sendSpacing{}, func(int64) int64 { return 0 })
	for i := 0; i < 3; i++ {
		if !paceOnce(p, context.Background()) {
			t.Fatalf("disabled pacer blocked a send")
		}
	}
	if len(*slept) != 0 {
		t.Fatalf("disabled pacer slept: %v", *slept)
	}
}

func TestSendPacerSpacesConsecutiveSends(t *testing.T) {
	// Fixed 1s gap; no wall time elapses between sends (instantaneous in the
	// fake), so every send after the first waits the full gap.
	p, slept := fakePacer(sendSpacing{min: time.Second, max: time.Second}, func(int64) int64 { return 0 })
	for i := 0; i < 3; i++ {
		if !paceOnce(p, context.Background()) {
			t.Fatalf("send %d unexpectedly blocked", i)
		}
	}
	want := []time.Duration{time.Second, time.Second}
	if len(*slept) != len(want) || (*slept)[0] != want[0] || (*slept)[1] != want[1] {
		t.Fatalf("slept = %v, want %v (no wait on first send, then the gap)", *slept, want)
	}
}

func TestSendPacerRandomGapWithinBounds(t *testing.T) {
	spacing := sendSpacing{min: 500 * time.Millisecond, max: 5 * time.Second}
	// rng returns 0 -> min; returns span -> max. Alternate to exercise both ends.
	span := int64(spacing.max - spacing.min)
	seq := []int64{0, span}
	i := 0
	p, slept := fakePacer(spacing, func(n int64) int64 {
		v := seq[i%len(seq)]
		i++
		if v >= n { // rng contract is [0,n); clamp for the max case
			v = n - 1
		}
		return v
	})
	for k := 0; k < 3; k++ {
		if !paceOnce(p, context.Background()) {
			t.Fatalf("send %d unexpectedly blocked", k)
		}
	}
	if len(*slept) != 2 {
		t.Fatalf("expected 2 waits, got %v", *slept)
	}
	for _, d := range *slept {
		if d < spacing.min || d > spacing.max {
			t.Fatalf("gap %s out of bounds [%s,%s]", d, spacing.min, spacing.max)
		}
	}
	if (*slept)[0] != spacing.min {
		t.Fatalf("first gap = %s, want min %s", (*slept)[0], spacing.min)
	}
}

func TestSendPacerFullDurationRangeDoesNotPanic(t *testing.T) {
	const maxDuration = time.Duration(1<<63 - 1)
	p := &sendPacer{
		spacing: sendSpacing{max: maxDuration},
		rng: func(n int64) int64 {
			if n <= 0 {
				t.Fatalf("rng bound = %d, want positive", n)
			}
			return n - 1
		},
	}
	if gap := p.pick(); gap < 0 || gap > maxDuration {
		t.Fatalf("gap = %s, want within [0,%s]", gap, maxDuration)
	}
}

func TestSendPacerSkipsWaitAfterIdlePeriodExceedsGap(t *testing.T) {
	// Advance the clock after the previous send completed. Existing idle time
	// counts toward the gap, so no additional wait is needed.
	clock := time.Unix(1000, 0)
	slept := []time.Duration{}
	p := &sendPacer{
		spacing: sendSpacing{min: time.Second, max: time.Second},
		now:     func() time.Time { return clock },
		sleep: func(_ context.Context, d time.Duration) {
			slept = append(slept, d)
			clock = clock.Add(d)
		},
		rng: func(int64) int64 { return 0 },
	}
	paceOnce(p, context.Background()) // first: records completion, no wait
	clock = clock.Add(3 * time.Second)
	paceOnce(p, context.Background()) // elapsed 3s > 1s gap: no wait
	if len(slept) != 0 {
		t.Fatalf("expected no wait when elapsed exceeds gap, slept %v", slept)
	}
}

func TestSendPacerStartsGapAfterSendCompletion(t *testing.T) {
	clock := time.Unix(1000, 0)
	var slept []time.Duration
	p := &sendPacer{
		spacing: sendSpacing{min: time.Second, max: time.Second},
		now:     func() time.Time { return clock },
		sleep: func(_ context.Context, d time.Duration) {
			slept = append(slept, d)
			clock = clock.Add(d)
		},
		rng: func(int64) int64 { return 0 },
	}

	if !p.wait(context.Background()) {
		t.Fatal("first send unexpectedly blocked")
	}
	clock = clock.Add(3 * time.Second) // slow preparation and wire send
	p.record()                         // handler records only after completion
	if !p.wait(context.Background()) {
		t.Fatal("second send unexpectedly blocked")
	}
	if len(slept) != 1 || slept[0] != time.Second {
		t.Fatalf("slept = %v, want one full gap after completion", slept)
	}
}

// Regression for the ClawSweeper P1 finding: if the caller's request timeout
// elapses while the pacer is waiting, the send must be aborted (not dispatched)
// and the spacing baseline must not move, so a paced send can never leave the
// wire after the caller has already reported failure.
func TestSendPacerAbortsWhenTimeoutElapsesDuringWait(t *testing.T) {
	clock := time.Unix(1000, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &sendPacer{
		spacing: sendSpacing{min: time.Second, max: time.Second},
		now:     func() time.Time { return clock },
		// The wait outlives the request budget: cancelling mid-sleep models the
		// caller's timeout firing before the gap is satisfied.
		sleep: func(_ context.Context, _ time.Duration) { cancel() },
		rng:   func(int64) int64 { return 0 },
	}

	if !paceOnce(p, ctx) { // first send dispatches immediately
		t.Fatalf("first send should dispatch")
	}
	baseline := p.last

	if dispatched := paceOnce(p, ctx); dispatched {
		t.Fatalf("second send dispatched after its wait was cut short by the request timeout")
	}
	if p.last != baseline {
		t.Fatalf("spacing baseline advanced for an un-dispatched send: last=%v baseline=%v", p.last, baseline)
	}
}

// An already-expired context must block even the first send: the caller has
// given up before we ever reach the wire.
func TestSendPacerFirstSendRespectsAlreadyExpiredContext(t *testing.T) {
	p, _ := fakePacer(sendSpacing{min: time.Second, max: time.Second}, func(int64) int64 { return 0 })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if paceOnce(p, ctx) {
		t.Fatalf("first send dispatched despite an already-cancelled context")
	}
}

func TestSendPacingTimeoutCancelsQueueWait(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	if err := clientConn.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set client deadline: %v", err)
	}

	var sendMu sync.Mutex
	pacedSendSlot := make(chan struct{}, 1) // empty means an earlier send owns it
	pacer := newSendPacer(sendSpacing{min: time.Second, max: time.Second})
	done := make(chan struct{})
	go func() {
		defer close(done)
		handleSendDelegateConn(context.Background(), serverConn, executeDelegatedSendWithoutApp, &sendMu, pacedSendSlot, pacer)
	}()

	req := sendDelegateRequest{
		Version:   sendDelegateVersion + 1,
		Kind:      "text",
		TimeoutMS: 10,
	}
	if err := json.NewEncoder(clientConn).Encode(req); err != nil {
		t.Fatalf("encode request: %v", err)
	}
	var resp sendDelegateResponse
	if err := json.NewDecoder(clientConn).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.OK || !strings.Contains(resp.Error, "send spacing exceeded request timeout") {
		t.Fatalf("response = %+v, want pre-dispatch spacing timeout", resp)
	}
	<-done
}

func TestSendPacingExtendsIPCDeadlineThroughResponse(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	trackedServerConn := &deadlineRecordingConn{Conn: serverConn}
	defer clientConn.Close()

	var sendMu sync.Mutex
	pacedSendSlot := make(chan struct{}, 1)
	pacedSendSlot <- struct{}{}
	pacer := newSendPacer(sendSpacing{min: time.Second, max: time.Second})
	done := make(chan struct{})
	go func() {
		defer close(done)
		handleSendDelegateConn(context.Background(), trackedServerConn, executeDelegatedSendWithoutApp, &sendMu, pacedSendSlot, pacer)
	}()

	const requestTimeout = 10 * time.Minute
	started := time.Now()
	req := sendDelegateRequest{
		Version:   sendDelegateVersion + 1,
		Kind:      "text",
		TimeoutMS: durationMillis(requestTimeout),
	}
	if err := json.NewEncoder(clientConn).Encode(req); err != nil {
		t.Fatalf("encode request: %v", err)
	}
	var resp sendDelegateResponse
	if err := json.NewDecoder(clientConn).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	<-done

	if len(trackedServerConn.deadlines) != 2 {
		t.Fatalf("deadlines = %v, want initial decode and paced request deadlines", trackedServerConn.deadlines)
	}
	wantAtLeast := started.Add(requestTimeout)
	if got := trackedServerConn.deadlines[1]; got.Before(wantAtLeast) {
		t.Fatalf("paced connection deadline = %s, want at least %s", got, wantAtLeast)
	}
}

func TestSendPacingHonorsAbsoluteCallerDeadline(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	if err := clientConn.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set client deadline: %v", err)
	}

	var sendMu sync.Mutex
	pacedSendSlot := make(chan struct{}, 1)
	pacedSendSlot <- struct{}{}
	pacer := newSendPacer(sendSpacing{min: time.Second, max: time.Second})
	done := make(chan struct{})
	go func() {
		defer close(done)
		handleSendDelegateConn(context.Background(), serverConn, executeDelegatedSendWithoutApp, &sendMu, pacedSendSlot, pacer)
	}()

	req := sendDelegateRequest{
		Version:        sendDelegateVersion + 1,
		Kind:           "text",
		TimeoutMS:      durationMillis(10 * time.Minute),
		DeadlineUnixMS: time.Now().Add(-time.Second).UnixMilli(),
	}
	if err := json.NewEncoder(clientConn).Encode(req); err != nil {
		t.Fatalf("encode request: %v", err)
	}
	var resp sendDelegateResponse
	if err := json.NewDecoder(clientConn).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.OK || !strings.Contains(resp.Error, "send spacing exceeded request timeout") {
		t.Fatalf("response = %+v, want expired caller deadline to block dispatch", resp)
	}
	<-done
}

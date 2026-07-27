package main

import (
	"context"
	"testing"
	"time"
)

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
// clock, so successive paces observe elapsed time deterministically.
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

func TestSendPacerDisabledNeverSleeps(t *testing.T) {
	p, slept := fakePacer(sendSpacing{}, func(int64) int64 { return 0 })
	for i := 0; i < 3; i++ {
		p.pace(context.Background())
	}
	if len(*slept) != 0 {
		t.Fatalf("disabled pacer slept: %v", *slept)
	}
}

func TestSendPacerSpacesConsecutiveSends(t *testing.T) {
	// Fixed 1s gap; no wall time elapses between paces (sends are instantaneous
	// in the fake), so every send after the first waits the full gap.
	p, slept := fakePacer(sendSpacing{min: time.Second, max: time.Second}, func(int64) int64 { return 0 })
	for i := 0; i < 3; i++ {
		p.pace(context.Background())
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
		p.pace(context.Background())
	}
	if len(*slept) != 2 {
		t.Fatalf("expected 2 waits, got %v", *slept)
	}
	for _, d := range *slept {
		if d < spacing.min || d > spacing.max {
			t.Fatalf("gap %s out of bounds [%s,%s]", d, spacing.min, spacing.max)
		}
	}
	// First waited gap should be the minimum (rng=0).
	if (*slept)[0] != spacing.min {
		t.Fatalf("first gap = %s, want min %s", (*slept)[0], spacing.min)
	}
}

func TestSendPacerSkipsWaitWhenElapsedExceedsGap(t *testing.T) {
	// A slow send: advance the clock past the gap before the next pace, so no
	// additional wait is needed.
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
	p.pace(context.Background()) // first: records start, no wait
	clock = clock.Add(3 * time.Second)
	p.pace(context.Background()) // elapsed 3s > 1s gap: no wait
	if len(slept) != 0 {
		t.Fatalf("expected no wait when elapsed exceeds gap, slept %v", slept)
	}
}

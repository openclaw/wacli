package main

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"time"
)

// sendSpacing is an optional minimum gap enforced between consecutive sends on
// a session. A range [min,max] draws a fresh random gap per send (jitter, which
// is what actually avoids a detectable automated cadence); min==max is a fixed
// gap. The zero value is disabled and preserves the default warn-only behavior.
type sendSpacing struct {
	min time.Duration
	max time.Duration
}

func (s sendSpacing) enabled() bool { return s.max > 0 }

// parseSendSpacing parses the --send-spacing value: "" (disabled), a single
// duration ("2s") for a fixed gap, or a "min-max" range ("500ms-5s") for a
// random gap per send. Bounds must be non-negative with min <= max.
func parseSendSpacing(raw string) (sendSpacing, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return sendSpacing{}, nil
	}
	lo, hi, isRange := strings.Cut(raw, "-")
	minD, err := time.ParseDuration(strings.TrimSpace(lo))
	if err != nil {
		return sendSpacing{}, fmt.Errorf("invalid --send-spacing %q: %w", raw, err)
	}
	maxD := minD
	if isRange {
		maxD, err = time.ParseDuration(strings.TrimSpace(hi))
		if err != nil {
			return sendSpacing{}, fmt.Errorf("invalid --send-spacing %q: %w", raw, err)
		}
	}
	if minD < 0 || maxD < 0 {
		return sendSpacing{}, fmt.Errorf("invalid --send-spacing %q: durations must be non-negative", raw)
	}
	if maxD < minD {
		return sendSpacing{}, fmt.Errorf("invalid --send-spacing %q: max must be >= min", raw)
	}
	return sendSpacing{min: minD, max: maxD}, nil
}

// sendPacer enforces sendSpacing across the daemon's serialized delegated
// sends. It is NOT safe for concurrent use: callers invoke wait and record while
// holding the send mutex, so sends are already serialized and the pacer just
// adds the gap between them. The clock, sleeper, and RNG are injectable for tests.
type sendPacer struct {
	spacing sendSpacing
	last    time.Time
	hasLast bool

	now   func() time.Time
	sleep func(context.Context, time.Duration)
	rng   func(n int64) int64
}

func newSendPacer(spacing sendSpacing) *sendPacer {
	src := rand.New(rand.NewSource(time.Now().UnixNano()))
	return &sendPacer{
		spacing: spacing,
		now:     time.Now,
		sleep:   sleepWithContext,
		rng:     src.Int63n,
	}
}

func (p *sendPacer) enabled() bool { return p != nil && p.spacing.enabled() }

// wait blocks until at least a freshly-chosen gap has elapsed since the
// previous serialized send attempt completed. It reports whether the caller
// may proceed with the send: false means the context was cancelled (the
// caller's request timeout elapsed) before the gap was satisfied, so the send
// MUST NOT be dispatched —
// otherwise pacing could push a send past the caller's timeout and deliver it
// after the caller has already reported failure. The first send of the session
// waits for nothing. Call inside the send critical section; on true, dispatch
// and call record() after the attempt completes.
func (p *sendPacer) wait(ctx context.Context) bool {
	if p == nil || !p.spacing.enabled() || !p.hasLast {
		return ctx.Err() == nil
	}
	gap := p.pick()
	if remaining := gap - p.now().Sub(p.last); remaining > 0 {
		p.sleep(ctx, remaining)
	}
	return ctx.Err() == nil
}

// record marks the completion of a serialized send attempt. Call it after
// wait() returned true and the attempt finishes, so time spent preparing or
// sending cannot consume the gap before the next attempt.
func (p *sendPacer) record() {
	if p == nil || !p.spacing.enabled() {
		return
	}
	p.last = p.now()
	p.hasLast = true
}

func (p *sendPacer) pick() time.Duration {
	if p.spacing.max <= p.spacing.min {
		return p.spacing.min
	}
	span := int64(p.spacing.max - p.spacing.min)
	// span+1 overflows only for the full non-negative time.Duration range.
	// Omitting that range's single upper endpoint keeps an otherwise valid CLI
	// value from panicking the daemon.
	if span == int64(time.Duration(1<<63-1)) {
		return p.spacing.min + time.Duration(p.rng(span))
	}
	return p.spacing.min + time.Duration(p.rng(span+1))
}

// sleepWithContext waits for d, returning early if ctx is cancelled.
func sleepWithContext(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

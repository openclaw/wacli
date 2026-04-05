package main

import "testing"

func TestParseLockOwnerPID(t *testing.T) {
	t.Run("extracts pid line", func(t *testing.T) {
		lockInfo := "pid=50394\nacquired_at=2026-04-05T12:30:11.568554+08:00"
		if got := parseLockOwnerPID(lockInfo); got != 50394 {
			t.Fatalf("parseLockOwnerPID() = %d, want 50394", got)
		}
	})

	t.Run("ignores missing pid", func(t *testing.T) {
		if got := parseLockOwnerPID("acquired_at=2026-04-05T12:30:11.568554+08:00"); got != 0 {
			t.Fatalf("parseLockOwnerPID() = %d, want 0", got)
		}
	})

	t.Run("ignores invalid pid", func(t *testing.T) {
		if got := parseLockOwnerPID("pid=abc"); got != 0 {
			t.Fatalf("parseLockOwnerPID() = %d, want 0", got)
		}
	})
}

package app

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

var lastHeartbeatWrite atomic.Int64

const heartbeatMinInterval = time.Minute

// writeHeartbeat persists the current timestamp to {storeDir}/HEARTBEAT,
// throttled to at most once per minute to avoid excessive I/O. The file
// lets external processes (e.g. wacli doctor) detect stale sync sessions.
func (a *App) writeHeartbeat() {
	now := nowUTC()
	last := time.Unix(0, lastHeartbeatWrite.Load())
	if now.Sub(last) < heartbeatMinInterval {
		return
	}
	lastHeartbeatWrite.Store(now.UnixNano())
	path := filepath.Join(a.opts.StoreDir, "HEARTBEAT")
	_ = os.WriteFile(path, []byte(now.Format(time.RFC3339)), 0o644)
}

// ReadHeartbeat reads the last heartbeat timestamp from the store directory.
// Returns zero time if the file does not exist or cannot be parsed.
func ReadHeartbeat(storeDir string) time.Time {
	path := filepath.Join(storeDir, "HEARTBEAT")
	data, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}
	}
	ts, err := time.Parse(time.RFC3339, string(data))
	if err != nil {
		return time.Time{}
	}
	return ts
}

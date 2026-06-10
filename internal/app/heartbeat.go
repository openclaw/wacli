package app

import (
	"os"
	"path/filepath"
	"time"

	"github.com/openclaw/wacli/internal/fsutil"
)

const heartbeatMinInterval = time.Minute

// writeHeartbeat persists the current timestamp to {storeDir}/HEARTBEAT,
// throttled to at most once per minute to avoid excessive I/O. The file
// lets external processes (e.g. wacli doctor) observe sync follow activity.
func (a *App) writeHeartbeat() {
	now := nowUTC()
	nowNanos := now.UnixNano()
	for {
		lastNanos := a.heartbeatLast.Load()
		last := time.Unix(0, lastNanos)
		if now.Sub(last) < heartbeatMinInterval {
			return
		}
		if a.heartbeatLast.CompareAndSwap(lastNanos, nowNanos) {
			break
		}
	}
	path := filepath.Join(a.opts.StoreDir, "HEARTBEAT")
	_ = fsutil.WritePrivateFileAtomic(path, []byte(now.Format(time.RFC3339)))
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

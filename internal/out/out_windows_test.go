//go:build windows

package out

import (
	"os"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWriteJSONWindowsBrokenPipe(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{name: "broken-pipe", err: windows.ERROR_BROKEN_PIPE},
		{name: "no-data", err: windows.ERROR_NO_DATA},
		{name: "errno-232", err: syscall.Errno(232)},
		{name: "os-path-error", err: &os.PathError{Op: "write", Path: "stdout", Err: windows.ERROR_NO_DATA}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := WriteJSON(errWriter{err: tc.err}, map[string]any{"ok": true})
			if err != nil {
				t.Fatalf("WriteJSON: %v", err)
			}
		})
	}
}

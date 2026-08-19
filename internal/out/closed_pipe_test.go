package out

import (
	"os"
	"syscall"
	"testing"
)

func TestIsWindowsClosedPipeErrno(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "broken-pipe", err: windowsErrorBrokenPipe, want: true},
		{name: "no-data", err: windowsErrorNoData, want: true},
		{name: "path-error-no-data", err: &os.PathError{Op: "write", Path: "stdout", Err: windowsErrorNoData}, want: true},
		{name: "epipe", err: syscall.EPIPE, want: false},
		{name: "other", err: syscall.EINVAL, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isWindowsClosedPipeErrno(tc.err); got != tc.want {
				t.Fatalf("isWindowsClosedPipeErrno(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

package out

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"
)

func TestWriteJSONEnvelope(t *testing.T) {
	var b bytes.Buffer
	if err := WriteJSON(&b, map[string]any{"ok": true}); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(b.String())), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["success"] != true {
		t.Fatalf("expected success=true, got %v", got["success"])
	}
	if got["error"] != nil {
		t.Fatalf("expected error=nil, got %v", got["error"])
	}
}

type errWriter struct {
	err error
}

func (w errWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func TestWriteJSONBrokenPipe(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{name: "epipe", err: syscall.EPIPE},
		{name: "os-path-error", err: &os.PathError{Op: "write", Path: "stdout", Err: syscall.EPIPE}},
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

func TestWriteJSONPreservesOtherWriteErrors(t *testing.T) {
	want := errors.New("disk full")
	err := WriteJSON(errWriter{err: want}, map[string]any{"ok": true})
	if !errors.Is(err, want) {
		t.Fatalf("WriteJSON error = %v, want %v", err, want)
	}
}

func TestWriteErrorJSONAndText(t *testing.T) {
	var b bytes.Buffer
	_ = WriteError(&b, true, errors.New("boom"))
	if !strings.Contains(b.String(), "\"success\":false") || !strings.Contains(b.String(), "boom") {
		t.Fatalf("unexpected json error output: %q", b.String())
	}

	b.Reset()
	_ = WriteError(&b, false, errors.New("boom\x1b[31m\nbad\x7f"))
	if strings.TrimSpace(b.String()) != "boom bad" {
		t.Fatalf("unexpected text error output: %q", b.String())
	}
}

func TestSanitizeBodyPreservesMessageLayout(t *testing.T) {
	got := SanitizeBody("one\n\ttwo\x1b[31m\rthree\x7f")
	if got != "one\n\ttwothree" {
		t.Fatalf("SanitizeBody = %q", got)
	}
}

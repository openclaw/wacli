package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow"
)

// fakeHTTPDoer satisfies httpDoer for tests; either resp or err is returned
// verbatim from Do.
type fakeHTTPDoer struct {
	resp *http.Response
	err  error
	last *http.Request
}

func (f *fakeHTTPDoer) Do(req *http.Request) (*http.Response, error) {
	f.last = req
	return f.resp, f.err
}

func TestParsePictureType(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      string
		preview bool
		wantErr bool
	}{
		{in: "", preview: true},
		{in: "preview", preview: true},
		{in: "PREVIEW", preview: true},
		{in: " preview ", preview: true},
		{in: "image", preview: false},
		{in: "IMAGE", preview: false},
		{in: "full", preview: false},
		{in: "thumbnail", wantErr: true},
		{in: "garbage", wantErr: true},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(fmt.Sprintf("type=%q", tc.in), func(t *testing.T) {
			t.Parallel()
			got, err := parsePictureType(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", tc.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.preview {
				t.Fatalf("preview = %v, want %v", got, tc.preview)
			}
		})
	}
}

func TestWrapProfilePictureErrorMapsSentinels(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   error
		want string
	}{
		{name: "nil-passthrough", in: nil, want: ""},
		{name: "not-set", in: whatsmeow.ErrProfilePictureNotSet, want: "no profile picture available"},
		{name: "wrapped-not-set", in: fmt.Errorf("outer: %w", whatsmeow.ErrProfilePictureNotSet), want: "no profile picture available"},
		{name: "unauthorized", in: whatsmeow.ErrProfilePictureUnauthorized, want: "not authorized to view this profile picture"},
		{name: "generic", in: fmt.Errorf("boom"), want: "get profile picture info: boom"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := wrapProfilePictureError(tc.in)
			if tc.want == "" {
				if got != nil {
					t.Fatalf("expected nil, got %v", got)
				}
				return
			}
			if got == nil || got.Error() != tc.want {
				t.Fatalf("got %v, want %q", got, tc.want)
			}
		})
	}
}

func TestDownloadProfilePictureSuccess(t *testing.T) {
	t.Parallel()
	payload := []byte("\xff\xd8\xff\xe0fakejpegbytes")
	doer := &fakeHTTPDoer{
		resp: &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(bytes.NewReader(payload)),
		},
	}
	got, err := downloadProfilePicture(t.Context(), doer, "https://pps.example.invalid/avatar.jpg")
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("got %q, want %q", got, payload)
	}
	if doer.last == nil || doer.last.URL.Host != "pps.example.invalid" {
		t.Fatalf("request URL not propagated: %v", doer.last)
	}
}

func TestDownloadProfilePictureHTTPError(t *testing.T) {
	t.Parallel()
	doer := &fakeHTTPDoer{
		resp: &http.Response{
			StatusCode: 404,
			Body:       io.NopCloser(strings.NewReader("not found")),
		},
	}
	_, err := downloadProfilePicture(t.Context(), doer, "https://pps.example.invalid/avatar.jpg")
	if err == nil {
		t.Fatal("expected HTTP 404 error")
	}
	if !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("error does not mention 404: %v", err)
	}
}

func TestDownloadProfilePictureRejectsOversize(t *testing.T) {
	t.Parallel()
	// Stream pictureMaxBytes+1 bytes to force the size guard to trip.
	body := bytes.Repeat([]byte{0xab}, pictureMaxBytes+1)
	doer := &fakeHTTPDoer{
		resp: &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(bytes.NewReader(body)),
		},
	}
	_, err := downloadProfilePicture(t.Context(), doer, "https://pps.example.invalid/avatar.jpg")
	if err == nil {
		t.Fatal("expected oversize rejection")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error does not mention oversize: %v", err)
	}
}

func TestDownloadProfilePictureTransportError(t *testing.T) {
	t.Parallel()
	doer := &fakeHTTPDoer{err: fmt.Errorf("connection refused")}
	_, err := downloadProfilePicture(t.Context(), doer, "https://pps.example.invalid/avatar.jpg")
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("expected wrapped transport error, got %v", err)
	}
}

func TestWriteProfilePictureToFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "alice.jpg")
	data := []byte("jpegcontents")

	resolved, n, err := writeProfilePicture(path, data)
	if err != nil {
		t.Fatalf("writeProfilePicture: %v", err)
	}
	if n != len(data) {
		t.Fatalf("wrote %d bytes, want %d", n, len(data))
	}
	if !filepath.IsAbs(resolved) {
		t.Fatalf("resolved should be absolute: %q", resolved)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("file contents differ: got %q, want %q", got, data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// Sanity-check owner-only perms; ignore on Windows (CI runs Linux/macOS).
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("perm = %o, want 0600", mode)
	}
}

func TestWriteProfilePictureRejectsBadPath(t *testing.T) {
	t.Parallel()
	// Writing into a non-existent nested directory should fail before clobbering.
	missing := filepath.Join(t.TempDir(), "does", "not", "exist", "avatar.jpg")
	_, _, err := writeProfilePicture(missing, []byte("x"))
	if err == nil {
		t.Fatal("expected error when parent directory is missing")
	}
}

func TestContactsCommandIncludesGetPicture(t *testing.T) {
	t.Parallel()
	cmd := newContactsCmd(&rootFlags{})
	if _, _, err := cmd.Find([]string{"get-picture"}); err != nil {
		t.Fatalf("missing contacts get-picture command: %v", err)
	}
}

func TestContactsGetPictureExposesFlags(t *testing.T) {
	t.Parallel()
	cmd := newContactsGetPictureCmd(&rootFlags{})
	for _, name := range []string{"jid", "output", "type", "existing-id"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Fatalf("missing --%s flag", name)
		}
	}
}

func TestContactsGetPictureRejectsConflictingStdoutJSON(t *testing.T) {
	t.Parallel()
	cmd := newContactsGetPictureCmd(&rootFlags{asJSON: true})
	cmd.SetArgs([]string{"--jid", "15551234567@s.whatsapp.net", "--output", "-"})
	// Silence cobra's usage spam on error.
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected --json + --output - to be rejected")
	}
	if !strings.Contains(err.Error(), "--json cannot be combined with --output -") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestContactsGetPictureRejectsMissingOutput(t *testing.T) {
	t.Parallel()
	cmd := newContactsGetPictureCmd(&rootFlags{})
	cmd.SetArgs([]string{"--jid", "15551234567@s.whatsapp.net"})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--output is required") {
		t.Fatalf("expected --output required error, got %v", err)
	}
}

func TestContactsGetPictureRejectsBadType(t *testing.T) {
	t.Parallel()
	cmd := newContactsGetPictureCmd(&rootFlags{})
	cmd.SetArgs([]string{"--jid", "15551234567@s.whatsapp.net", "--output", "/tmp/x.jpg", "--type", "garbage"})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--type must be one of") {
		t.Fatalf("expected bad --type error, got %v", err)
	}
}

func TestContactsGetPictureOutputJSONShape(t *testing.T) {
	t.Parallel()
	result := contactsGetPictureOutput{
		JID:    "15551234567@s.whatsapp.net",
		ID:     "abc123",
		Type:   "image",
		URL:    "https://pps.example.invalid/avatar.jpg",
		Output: "/tmp/alice.jpg",
		Bytes:  4321,
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(data)
	for _, field := range []string{`"jid":"15551234567@s.whatsapp.net"`, `"id":"abc123"`, `"type":"image"`, `"url":"https://pps.example.invalid/avatar.jpg"`, `"output":"/tmp/alice.jpg"`, `"bytes":4321`} {
		if !strings.Contains(got, field) {
			t.Fatalf("missing %s in JSON: %s", field, got)
		}
	}
}

func TestContactsGetPictureOutputJSONOmitsEmptyOptionals(t *testing.T) {
	t.Parallel()
	result := contactsGetPictureOutput{
		JID:    "15551234567@s.whatsapp.net",
		Output: "-",
		Bytes:  0,
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(data)
	for _, omit := range []string{`"id"`, `"type"`, `"url"`} {
		if strings.Contains(got, omit) {
			t.Fatalf("expected %s to be omitted, got %s", omit, got)
		}
	}
}

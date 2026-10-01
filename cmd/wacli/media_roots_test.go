package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeMediaRootFile(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("media"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCheckOutboundMediaPath(t *testing.T) {
	outbox := t.TempDir()
	other := t.TempDir()
	inside := writeMediaRootFile(t, outbox, "photo.jpg")
	outside := writeMediaRootFile(t, other, "secret.txt")
	if err := os.Mkdir(filepath.Join(outbox, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	nested := writeMediaRootFile(t, filepath.Join(outbox, "nested"), "doc.pdf")
	escape := filepath.Join(outbox, "escape.txt")
	if err := os.Symlink(outside, escape); err != nil {
		t.Fatal(err)
	}
	traversal := filepath.Join(outbox, "..", filepath.Base(other), "secret.txt")

	for _, tc := range []struct {
		name    string
		roots   string
		path    string
		wantErr string
	}{
		{name: "unset allows anything", roots: "", path: outside},
		{name: "file in root", roots: outbox, path: inside},
		{name: "file in nested dir", roots: outbox, path: nested},
		{name: "file outside root", roots: outbox, path: outside, wantErr: "is outside WACLI_MEDIA_ROOTS"},
		{name: "symlink escaping root", roots: outbox, path: escape, wantErr: "is outside WACLI_MEDIA_ROOTS"},
		{name: "dot-dot traversal", roots: outbox, path: traversal, wantErr: "is outside WACLI_MEDIA_ROOTS"},
		{name: "second root", roots: other + string(os.PathListSeparator) + outbox, path: inside},
		{name: "missing root is skipped", roots: filepath.Join(outbox, "missing") + string(os.PathListSeparator) + outbox, path: inside},
		{name: "root itself is not a file", roots: outbox, path: outbox, wantErr: "is outside WACLI_MEDIA_ROOTS"},
		{name: "relative root after matching root rejected", roots: outbox + string(os.PathListSeparator) + "relative", path: inside, wantErr: "must be absolute"},
		{name: "nested directory is not a file", roots: outbox, path: filepath.Join(outbox, "nested"), wantErr: "not a regular file"},
		{name: "relative root rejected", roots: "outbox", path: inside, wantErr: "must be absolute"},
		{name: "missing file", roots: outbox, path: filepath.Join(outbox, "nope.jpg"), wantErr: "resolve"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(mediaRootsEnv, tc.roots)
			err := checkOutboundMediaPath(tc.path)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("checkOutboundMediaPath(%s) = %v, want nil", tc.path, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("checkOutboundMediaPath(%s) = %v, want error containing %q", tc.path, err, tc.wantErr)
			}
		})
	}
}

// A root reached through a symlinked directory, such as /tmp on macOS, still
// matches files addressed through its resolved path.
func TestCheckOutboundMediaPathSymlinkedRoot(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "outbox")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	file := writeMediaRootFile(t, real, "photo.jpg")
	t.Setenv(mediaRootsEnv, link)
	if err := checkOutboundMediaPath(file); err != nil {
		t.Fatalf("file under symlinked root rejected: %v", err)
	}
	if data, err := readSendFileData(filepath.Join(link, "photo.jpg")); err != nil || string(data) != "media" {
		t.Fatalf("upload through symlinked root = %q, %v", data, err)
	}
	if err := checkOutboundMediaPath(filepath.Join(link, "photo.jpg")); err != nil {
		t.Fatalf("file addressed through symlinked root rejected: %v", err)
	}
}

// The check runs before the store is opened or a running sync daemon is asked
// to send, so a refused file never reaches WhatsApp.
func TestSendCommandsRefuseFilesOutsideMediaRoots(t *testing.T) {
	outbox := t.TempDir()
	outside := writeMediaRootFile(t, t.TempDir(), "secret.ogg")
	t.Setenv(mediaRootsEnv, outbox)
	t.Setenv("WACLI_READONLY", "")
	for _, args := range [][]string{
		{"send", "file", "--to", "15550000001", "--file", outside},
		{"send", "voice", "--to", "15550000001", "--file", outside},
		{"send", "sticker", "--to", "15550000001", "--file", outside},
		{"send", "status", "--file", outside},
	} {
		t.Run(args[1], func(t *testing.T) {
			storeDir := filepath.Join(t.TempDir(), "store")
			err := execute(append([]string{"--store", storeDir}, args...))
			if err == nil || !strings.Contains(err.Error(), "is outside WACLI_MEDIA_ROOTS") {
				t.Fatalf("%v: error = %v, want media roots refusal", args, err)
			}
			if _, statErr := os.Stat(storeDir); !os.IsNotExist(statErr) {
				t.Fatalf("%v: store was opened before the media roots check (stat err %v)", args, statErr)
			}
		})
	}
}

func TestUploadReadEnforcesMediaRootsAfterPreflight(t *testing.T) {
	root := t.TempDir()
	outside := writeMediaRootFile(t, t.TempDir(), "secret.txt")
	file := writeMediaRootFile(t, root, "upload.txt")
	t.Setenv(mediaRootsEnv, root)
	if err := checkOutboundMediaPath(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, file); err != nil {
		t.Fatal(err)
	}
	if _, err := readSendFileData(file); err == nil || !strings.Contains(err.Error(), "WACLI_MEDIA_ROOTS") {
		t.Fatalf("upload read after symlink swap = %v, want confinement error", err)
	}
}

func TestMediaRootsEmptyListFailsClosed(t *testing.T) {
	file := writeMediaRootFile(t, t.TempDir(), "secret.txt")
	for _, roots := range []string{string(os.PathListSeparator), " ", "\t\n"} {
		t.Setenv(mediaRootsEnv, roots)
		if err := checkOutboundMediaPath(file); err == nil {
			t.Errorf("nonempty root list %q allowed every file", roots)
		}
		if _, err := readSendFileData(file); err == nil {
			t.Errorf("upload read allowed empty root list %q", roots)
		}
	}
}

func TestMediaRootsPreserveDirectoryWhitespace(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("Windows does not preserve trailing spaces in directory names")
	}
	base := t.TempDir()
	root := filepath.Join(base, "outbox ")
	sibling := filepath.Join(base, "outbox")
	for _, dir := range []string{root, sibling} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	inside := writeMediaRootFile(t, root, "inside.txt")
	outside := writeMediaRootFile(t, sibling, "outside.txt")
	t.Setenv(mediaRootsEnv, root)
	if _, err := readSendFileData(inside); err != nil {
		t.Fatalf("configured path changed: %v", err)
	}
	if _, err := readSendFileData(outside); err == nil {
		t.Fatal("whitespace trimming widened root to sibling")
	}
}

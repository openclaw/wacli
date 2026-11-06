package delegate

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestPipePathResolvesStoreAlias(t *testing.T) {
	dir := t.TempDir()
	alias := filepath.Join(t.TempDir(), "store-link")
	if err := os.Symlink(dir, alias); err != nil {
		t.Skipf("creating directory links requires Windows privileges: %v", err)
	}
	want, err := pipePath(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pipePath(alias)
	if err != nil || got != want {
		t.Fatalf("alias pipe = %q, err = %v, want %q", got, err, want)
	}
}

func TestPipeSecurityAndDeadline(t *testing.T) {
	dir := t.TempDir()
	ln, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	acceptErr := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			acceptErr <- err
			return
		}
		accepted <- conn
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := Dial(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var server net.Conn
	select {
	case server = <-accepted:
	case err := <-acceptErr:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	defer server.Close()
	handle := windows.Handle(server.(interface{ Fd() uintptr }).Fd())
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil || dacl.AceCount != 1 {
		t.Fatalf("expected one explicit user ACL entry: %v, %v", dacl, err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil {
		t.Fatal(err)
	}
	// SDDL renders well-known users as aliases (for example Administrator as LA).
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || !(*windows.SID)(unsafe.Pointer(&ace.SidStart)).Equals(user.User.Sid) {
		t.Fatalf("pipe DACL is not for the current user: %s", sd.String())
	}
	if err := client.SetReadDeadline(time.Now().Add(30 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	_, err = client.Read(make([]byte, 1))
	if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
		t.Fatalf("read should time out: %v", err)
	}
}

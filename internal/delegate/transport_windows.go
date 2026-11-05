package delegate

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"path/filepath"
	"strings"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

func pipePath(storeDir string) (string, error) {
	path, err := filepath.Abs(storeDir)
	if err != nil {
		return "", err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`\\.\pipe\wacli-send-%x`, sha256.Sum256([]byte(strings.ToLower(path)))), nil
}

func Dial(ctx context.Context, storeDir string) (net.Conn, error) {
	path, err := pipePath(storeDir)
	if err != nil {
		return nil, err
	}
	return winio.DialPipeContext(ctx, path)
}

func Listen(storeDir string) (net.Listener, error) {
	path, err := pipePath(storeDir)
	if err != nil {
		return nil, err
	}
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("get delegate pipe owner: %w", err)
	}
	// Do not touch legacy AF_UNIX files: Windows may refuse even Lstat on them.
	// The protected DACL grants access only to this process's Windows user.
	return winio.ListenPipe(path, &winio.PipeConfig{
		SecurityDescriptor: "D:P(A;;GA;;;" + user.User.Sid.String() + ")",
	})
}

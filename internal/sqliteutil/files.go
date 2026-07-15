package sqliteutil

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// ChmodFiles applies mode to a SQLite database and existing WAL/SHM sidecars.
func ChmodFiles(path string, mode os.FileMode) error {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		p := path + suffix
		if err := os.Chmod(p, mode); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("chmod %s: %w", filepath.Base(p), err)
		}
	}
	return nil
}

func FileURI(path, rawQuery string) string {
	return (&url.URL{Scheme: "file", Path: sqliteFileURLPath(path), RawQuery: rawQuery}).String()
}

func sqliteFileURLPath(path string) string {
	if len(path) >= 3 && isASCIIAlpha(path[0]) && path[1] == ':' && (path[2] == '\\' || path[2] == '/') {
		return "/" + strings.ReplaceAll(path, `\`, "/")
	}
	if strings.HasPrefix(path, `\\`) {
		return strings.ReplaceAll(path, `\`, "/")
	}
	return path
}

func isASCIIAlpha(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}

package fsutil

import (
	"fmt"
	"io"
	"os"
)

func WritePrivateFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("open private file: %w", err)
	}
	chmodErr := f.Chmod(0o600)
	if chmodErr == nil {
		chmodErr = f.Truncate(0)
	}
	if chmodErr == nil {
		_, chmodErr = f.Seek(0, 0)
	}
	var writeErr error
	if chmodErr == nil {
		n, err := f.Write(data)
		if err == nil && n != len(data) {
			err = io.ErrShortWrite
		}
		writeErr = err
	}
	closeErr := f.Close()
	if chmodErr != nil {
		return fmt.Errorf("chmod private file: %w", chmodErr)
	}
	if writeErr != nil {
		return fmt.Errorf("write private file: %w", writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close private file: %w", closeErr)
	}
	return nil
}

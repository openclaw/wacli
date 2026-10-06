package main

import (
	"fmt"
	"io"
	"os"
)

func readRegularFileLimited(path string, maxBytes int64) ([]byte, error) {
	return readRegularFileLimitedWithOpener(path, maxBytes, os.Open)
}

func readRegularFileLimitedWithOpener(path string, maxBytes int64, open func(string) (*os.File, error)) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if info.Size() > maxBytes {
		return nil, fmt.Errorf("file too large (%d bytes); maximum size is %d bytes", info.Size(), maxBytes)
	}

	f, err := open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err = f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}

	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("file too large; maximum size is %d bytes", maxBytes)
	}
	return data, nil
}

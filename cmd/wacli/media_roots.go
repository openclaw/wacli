package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const mediaRootsEnv = "WACLI_MEDIA_ROOTS"

func outboundMediaRoots() ([]string, error) {
	raw := os.Getenv(mediaRootsEnv)
	if raw == "" {
		return nil, nil
	}
	var roots []string
	for _, root := range filepath.SplitList(raw) {
		if root == "" {
			continue
		}
		if !filepath.IsAbs(root) {
			return nil, fmt.Errorf("%s entries must be absolute paths, got %q", mediaRootsEnv, root)
		}
		roots = append(roots, root)
	}
	if len(roots) == 0 {
		return nil, fmt.Errorf("%s contains no directories", mediaRootsEnv)
	}
	return roots, nil
}

// Refuse restricted paths before opening the store or delegating a send.
func checkOutboundMediaPath(path string) error {
	roots, err := outboundMediaRoots()
	if err != nil || len(roots) == 0 {
		return err
	}
	file, err := openMediaWithinRoots(path, roots)
	if err != nil {
		return err
	}
	return file.Close()
}

// Recheck at the upload read, including reads performed by a sync daemon.
func openOutboundMedia(path string) (*os.File, error) {
	roots, err := outboundMediaRoots()
	if err != nil {
		return nil, err
	}
	if len(roots) == 0 {
		return os.Open(path)
	}
	return openMediaWithinRoots(path, roots)
}

func openMediaWithinRoots(path string, roots []string) (*os.File, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", path, err)
	}
	for _, root := range roots {
		realRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		if !pathWithin(realRoot, real) {
			continue
		}
		info, err := os.Stat(real)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%s is not a regular file", path)
		}
		dir, err := os.OpenRoot(realRoot)
		if err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(realRoot, real)
		if err != nil {
			dir.Close()
			return nil, err
		}
		// OpenRoot keeps a path replaced after validation from escaping the root.
		file, err := dir.Open(rel)
		dir.Close()
		if err != nil {
			return nil, fmt.Errorf("open media within %s: %w", mediaRootsEnv, err)
		}
		return file, nil
	}
	return nil, fmt.Errorf("%s is outside %s; copy it into one of: %s", path, mediaRootsEnv, strings.Join(roots, ", "))
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

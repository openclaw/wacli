package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/wa"
)

func TestCopyVerifiedMediaCache(t *testing.T) {
	for _, name := range []string{"valid", "empty content", "unknown length", "empty path", "missing", "directory", "no fingerprint", "short fingerprint", "hash mismatch", "length mismatch", "oversized", "oversized declaration"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			content := []byte("verified plaintext media")
			if name == "empty content" {
				content = nil
			}
			hash := sha256.Sum256(content)
			source, target := filepath.Join(dir, "source"), filepath.Join(dir, "target")
			if err := os.WriteFile(source, content, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, []byte("keep original"), 0600); err != nil {
				t.Fatal(err)
			}
			info := store.MediaDownloadInfo{LocalPath: source, FileSHA256: hash[:], FileLength: uint64(len(content))}
			wantHit := name == "valid" || name == "unknown length" || name == "empty content"
			switch name {
			case "unknown length":
				info.FileLength = 0
			case "empty path":
				info.LocalPath = ""
			case "missing":
				info.LocalPath = filepath.Join(dir, "missing")
			case "directory":
				info.LocalPath = dir
			case "no fingerprint":
				info.FileSHA256 = nil
			case "short fingerprint":
				info.FileSHA256 = hash[:5]
			case "hash mismatch":
				info.FileSHA256 = make([]byte, sha256.Size)
			case "length mismatch":
				info.FileLength++
			case "oversized":
				if err := os.Truncate(source, wa.MaxMediaDownloadSize+1); err != nil {
					t.Fatal(err)
				}
			case "oversized declaration":
				info.FileLength = wa.MaxMediaDownloadSize + 1
			}
			n, hit, err := CopyVerifiedMediaCache(context.Background(), info, target)
			if err != nil || hit != wantHit {
				t.Fatalf("copy = %d, %v, %v; want hit %v", n, hit, err, wantHit)
			}
			want := "keep original"
			if wantHit {
				want = string(content)
				if n != int64(len(content)) {
					t.Fatalf("bytes = %d", n)
				}
			} else if n != 0 {
				t.Fatalf("miss bytes = %d", n)
			}
			got, err := os.ReadFile(target)
			if err != nil || string(got) != want {
				t.Fatalf("target = %q, %v; want %q", got, err, want)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 2 {
				t.Fatalf("temporary file leaked: %v", entries)
			}
			if wantHit {
				st, err := os.Stat(target)
				if err != nil {
					t.Fatal(err)
				}
				if st.Mode().Perm() != 0600 {
					t.Fatalf("target permissions = %o", st.Mode().Perm())
				}
			}
		})
	}
}

func TestCopyVerifiedMediaCacheSameFile(t *testing.T) {
	for _, hardlink := range []bool{false, true} {
		t.Run(map[bool]string{false: "same path", true: "hardlink"}[hardlink], func(t *testing.T) {
			dir := t.TempDir()
			content := []byte("must never truncate")
			hash := sha256.Sum256(content)
			source := filepath.Join(dir, "source")
			if err := os.WriteFile(source, content, 0600); err != nil {
				t.Fatal(err)
			}
			target := source
			if hardlink {
				target = filepath.Join(dir, "linked")
				if err := os.Link(source, target); err != nil {
					t.Fatal(err)
				}
			}
			info := store.MediaDownloadInfo{LocalPath: source, FileSHA256: hash[:], FileLength: uint64(len(content))}
			n, hit, err := CopyVerifiedMediaCache(context.Background(), info, target)
			if err != nil || !hit || n != int64(len(content)) {
				t.Fatalf("copy = %d, %v, %v", n, hit, err)
			}
			info.FileSHA256 = make([]byte, sha256.Size)
			_, hit, err = CopyVerifiedMediaCache(context.Background(), info, target)
			if err != nil || hit {
				t.Fatalf("corrupt same-file copy = %v, %v", hit, err)
			}
			got, err := os.ReadFile(source)
			if err != nil || string(got) != string(content) {
				t.Fatalf("source = %q, %v", got, err)
			}
		})
	}
}

func TestCopyVerifiedMediaCacheErrorsPreserveTarget(t *testing.T) {
	dir := t.TempDir()
	content := []byte("verified plaintext")
	hash := sha256.Sum256(content)
	source, target := filepath.Join(dir, "source"), filepath.Join(dir, "target")
	if err := os.WriteFile(source, content, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("keep original"), 0600); err != nil {
		t.Fatal(err)
	}
	info := store.MediaDownloadInfo{LocalPath: source, FileSHA256: hash[:]}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, hit, err := CopyVerifiedMediaCache(ctx, info, target); hit || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled copy = %v, %v", hit, err)
	}
	if _, hit, err := CopyVerifiedMediaCache(context.Background(), info, filepath.Join(source, "target")); hit || err == nil {
		t.Fatalf("destination error = %v, %v", hit, err)
	}
	if _, hit, err := CopyVerifiedMediaCache(context.Background(), info, dir); hit || err == nil {
		t.Fatalf("rename error = %v, %v", hit, err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "keep original" {
		t.Fatalf("target = %q, %v", got, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("temporary file leaked: %v, %v", entries, err)
	}
}

func TestCopyVerifiedMediaCacheCreatesOutputDirectoryOnlyAfterVerification(t *testing.T) {
	dir := t.TempDir()
	content := []byte("verified nested output")
	hash := sha256.Sum256(content)
	source := filepath.Join(dir, "source")
	if err := os.WriteFile(source, content, 0600); err != nil {
		t.Fatal(err)
	}
	info := store.MediaDownloadInfo{LocalPath: source, FileSHA256: make([]byte, sha256.Size)}
	target := filepath.Join(dir, "nested", "output", "target")
	if n, hit, err := CopyVerifiedMediaCache(context.Background(), info, target); err != nil || hit || n != 0 {
		t.Fatalf("invalid cache copy = %d, %v, %v", n, hit, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "nested")); !os.IsNotExist(err) {
		t.Fatalf("invalid cache created output directory: %v", err)
	}
	info.FileSHA256 = hash[:]
	if n, hit, err := CopyVerifiedMediaCache(context.Background(), info, target); err != nil || !hit || n != int64(len(content)) {
		t.Fatalf("valid nested copy = %d, %v, %v", n, hit, err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != string(content) {
		t.Fatalf("nested output = %q, %v", got, err)
	}
}

package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"

	"github.com/openclaw/wacli/internal/fsutil"
	"github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/wa"
)

// CopyVerifiedMediaCache publishes only cached plaintext matching the message's
// fingerprint. A cache miss leaves target intact and permits a network fallback;
// destination failures and cancellation remain errors for the caller to surface.
func CopyVerifiedMediaCache(ctx context.Context, info store.MediaDownloadInfo, target string) (int64, bool, error) {
	if err := ctx.Err(); err != nil {
		return 0, false, err
	}
	if info.LocalPath == "" || len(info.FileSHA256) != sha256.Size || info.FileLength > wa.MaxMediaDownloadSize {
		return 0, false, nil
	}
	// Check before opening to avoid blocking on a directory or FIFO. Recheck the
	// opened descriptor so path replacement cannot substitute a nonregular file.
	st, err := os.Stat(info.LocalPath)
	if err != nil || !st.Mode().IsRegular() || st.Size() > wa.MaxMediaDownloadSize {
		return 0, false, nil
	}
	source, err := os.Open(info.LocalPath)
	if err != nil {
		return 0, false, nil
	}
	defer source.Close()
	st, err = source.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > wa.MaxMediaDownloadSize {
		return 0, false, nil
	}

	// Verify before creating output: an unusable cache is a miss regardless of
	// destination availability. Same-path and hardlink hits never open for writing.
	n, digest, err := scanMediaCache(ctx, source, nil)
	if err != nil {
		if ctx.Err() != nil {
			return 0, false, ctx.Err()
		}
		return 0, false, nil
	}
	if !mediaCacheMatches(info, n, digest) {
		return 0, false, nil
	}
	if dst, err := os.Stat(target); err == nil && os.SameFile(st, dst) {
		return n, true, nil
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return 0, false, nil
	}

	// CreateTemp supplies owner-only permissions. Keeping it beside the target
	// makes rename atomic, so failed copies cannot destroy an existing output.
	// Match downloader directory creation only after cache validity is proven.
	if err := fsutil.EnsureWritableDir(filepath.Dir(target)); err != nil {
		return 0, false, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".wacli-media-cache-*")
	if err != nil {
		return 0, false, err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	n, digest, err = scanMediaCache(ctx, source, tmp)
	if err != nil {
		if _, sourceFailure := err.(mediaCacheReadError); sourceFailure && ctx.Err() == nil {
			return 0, false, nil
		}
		return 0, false, err
	}
	if !mediaCacheMatches(info, n, digest) {
		return 0, false, nil
	}
	// Read back the actual output bytes, not merely the bytes supplied to Write.
	// This also rejects a source that changed between verification and copying.
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return 0, false, err
	}
	n, digest, err = scanMediaCache(ctx, tmp, nil)
	if err != nil {
		return 0, false, err
	}
	if !mediaCacheMatches(info, n, digest) {
		return 0, false, nil
	}
	// Match the native downloader's durable-write boundary before publication.
	if err := tmp.Sync(); err != nil {
		return 0, false, err
	}
	if err := tmp.Close(); err != nil {
		return 0, false, err
	}
	if err := ctx.Err(); err != nil {
		return 0, false, err
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		return 0, false, err
	}
	return n, true, nil
}

func mediaCacheMatches(info store.MediaDownloadInfo, n int64, digest []byte) bool {
	return n <= wa.MaxMediaDownloadSize && (info.FileLength == 0 || uint64(n) == info.FileLength) && bytes.Equal(digest, info.FileSHA256)
}

// Preserve the distinction between an unreadable cache and unwritable output.
type mediaCacheReadError struct{ error }

func (err mediaCacheReadError) Unwrap() error { return err.error }

// scanMediaCache uses fixed memory and reads at most one byte beyond the bound,
// even if the file grows after Stat. Cancellation is checked between chunks.
func scanMediaCache(ctx context.Context, source *os.File, output io.Writer) (int64, []byte, error) {
	hash := sha256.New()
	buf := make([]byte, 32*1024)
	var total int64
	for total <= wa.MaxMediaDownloadSize {
		if err := ctx.Err(); err != nil {
			return 0, nil, err
		}
		remaining := int64(wa.MaxMediaDownloadSize) + 1 - total
		chunk := buf
		if remaining < int64(len(chunk)) {
			chunk = chunk[:remaining]
		}
		n, err := source.Read(chunk)
		if n > 0 {
			total += int64(n)
			if total > wa.MaxMediaDownloadSize {
				return total, nil, nil
			}
			if output != nil {
				written, writeErr := output.Write(chunk[:n])
				if writeErr != nil {
					return 0, nil, writeErr
				}
				if written != n {
					return 0, nil, io.ErrShortWrite
				}
			}
			_, _ = hash.Write(chunk[:n])
		}
		if err == io.EOF {
			return total, hash.Sum(nil), ctx.Err()
		}
		if err != nil {
			return 0, nil, mediaCacheReadError{err}
		}
	}
	return total, nil, nil
}

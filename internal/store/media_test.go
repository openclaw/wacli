package store

import (
	"context"
	"errors"
	"testing"
)

func TestPendingMediaQueriesHonorCanceledContext(t *testing.T) {
	db := openTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := db.CountPendingMediaDownloads(ctx, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("CountPendingMediaDownloads error = %v, want context.Canceled", err)
	}
	if _, err := db.ListPendingMediaDownloads(ctx, "", 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListPendingMediaDownloads error = %v, want context.Canceled", err)
	}
}

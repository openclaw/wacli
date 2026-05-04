package app

import (
	"sync"
	"testing"
)

func TestOpenWAConcurrentInitialization(t *testing.T) {
	a := newTestApp(t)

	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- a.OpenWA()
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("OpenWA: %v", err)
		}
	}
	if a.WA() == nil {
		t.Fatal("WA client was not initialized")
	}
}

func newTestApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	a, err := New(Options{StoreDir: dir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}

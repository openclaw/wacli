package app

import (
	"sync"
	"testing"
)

func TestOpenWARaceCondition(t *testing.T) {
	a := newTestApp(t)

	var wg sync.WaitGroup
	errs := make([]error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = a.OpenWA()
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: unexpected error: %v", i, err)
		}
	}

	wa := a.WA()
	if wa == nil {
		t.Fatal("expected WA client to be initialized")
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

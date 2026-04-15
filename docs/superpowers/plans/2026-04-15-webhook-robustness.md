# Webhook Robustness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Improve webhook reliability by using a shared HTTP client and implementing async retries with exponential backoff.

**Architecture:** Initialize a shared `*http.Client` in the `App` struct. Modify the hook worker loop to handle retry scheduling via a new `retryChan` and `time.AfterFunc`, ensuring non-blocking behavior for the main sync loop.

**Tech Stack:** Go 1.21+, `net/http` (shared client), `time` (backoff), `crypto/hmac` (existing signature logic).

---

## File Map

| File | Action | Responsibility |
|------|--------|---------------|
| `internal/app/app.go` | Modify | Add `httpClient` to `App` struct; initialize in `New` |
| `internal/app/sync.go` | Modify | Update `dispatchHooks` to use shared client and handle retries; define `webhookJob` |
| `internal/app/sync_test.go` | Modify | Add tests for retry logic and shared client usage |
| `cmd/wacli/sync.go` | Modify | Add `--webhook-max-retries` and `--webhook-retry-delay` flags |

---

## Task 1: Shared HTTP Client

**Files:**
- Modify: `internal/app/app.go`

- [ ] **Step 1: Add `httpClient` to `App` struct**

Update `App` struct in `internal/app/app.go`:

```go
type App struct {
	opts       Options
	wa         WAClient
	db         *store.DB
	hookChan   chan parsedMessageJob
	httpClient *http.Client // shared client for webhooks
}
```

- [ ] **Step 2: Initialize `httpClient` in `New`**

Update `New` function in `internal/app/app.go`:

```go
func New(opts Options) (*App, error) {
	// ... existing code ...
	return &App{
		opts: opts,
		db:   db,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}, nil
}
```

- [ ] **Step 3: Verify build**

Run: `/usr/local/go/bin/go build ./internal/app/...`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add internal/app/app.go
git commit -m "refactor: add shared http.Client to App struct"
```

---

## Task 2: Refactor Hook Jobs and dispatchHooks

**Files:**
- Modify: `internal/app/sync.go`
- Modify: `internal/app/app.go`

- [ ] **Step 1: Update `parsedMessageJob` to include attempt count**

Modify `internal/app/app.go`:

```go
type parsedMessageJob struct {
	pm       wa.ParsedMessage
	opts     SyncOptions
	attempts int // number of previous failed attempts
}
```

- [ ] **Step 2: Update `dispatchHooks` to use shared client**

Modify `internal/app/sync.go` (around `dispatchHooks`):

```go
func (a *App) dispatchHooks(ctx context.Context, opts SyncOptions, pm wa.ParsedMessage, attempts int) {
    // ... marshaling ...
    
    // Webhook Hook
    if opts.WebhookURL != "" {
        req, err := http.NewRequestWithContext(ctx, "POST", opts.WebhookURL, bytes.NewReader(data))
        // ... headers ...
        
        resp, err := a.httpClient.Do(req) // Use shared client
        // ... handle response ...
    }
}
```

- [ ] **Step 3: Update `StartHookWorkers` to pass 0 attempts**

Modify `internal/app/app.go`:

```go
func (a *App) StartHookWorkers(ctx context.Context, numWorkers int) {
    // ...
    a.dispatchHooks(ctx, job.opts, job.pm, job.attempts)
    // ...
}
```

- [ ] **Step 4: Verify build**

Run: `/usr/local/go/bin/go build ./internal/app/...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/app/app.go internal/app/sync.go
git commit -m "refactor: include attempts in parsedMessageJob and use shared client"
```

---

## Task 3: Implement Async Retry with Backoff

**Files:**
- Modify: `internal/app/sync.go`
- Test: `internal/app/sync_test.go`

- [ ] **Step 1: Write failing test for retry**

Add `TestDispatchWebhook_Retry` to `internal/app/sync_test.go`:

```go
func TestDispatchWebhook_Retry(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := attempts.Add(1)
		if count < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a, _ := New(Options{StoreDir: t.TempDir()})
	a.hookChan = make(chan parsedMessageJob, 10)
	
	opts := SyncOptions{
		WebhookURL: srv.URL,
		WebhookMaxRetries: 3,
		WebhookRetryDelay: 10 * time.Millisecond,
	}
	pm := wa.ParsedMessage{ID: "retry-test"}

	// First attempt
	a.dispatchHooks(context.Background(), opts, pm, 0)

	// Wait for retries
	time.Sleep(100 * time.Millisecond)

	if attempts.Load() != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts.Load())
	}
}
```

- [ ] **Step 2: Run test to verify failure**

Run: `/usr/local/go/bin/go test ./internal/app/... -v -run "TestDispatchWebhook_Retry"`
Expected: FAIL (only 1 attempt made)

- [ ] **Step 3: Update `SyncOptions` with retry fields**

Modify `internal/app/sync.go`:

```go
type SyncOptions struct {
    // ...
    WebhookMaxRetries int
    WebhookRetryDelay time.Duration
}
```

- [ ] **Step 4: Implement retry logic in `dispatchHooks`**

Modify `internal/app/sync.go`:

```go
func (a *App) dispatchHooks(ctx context.Context, opts SyncOptions, pm wa.ParsedMessage, attempts int) {
    // ...
    resp, err := a.httpClient.Do(req)
    if err != nil || (resp != nil && resp.StatusCode >= 400) {
        if attempts < opts.WebhookMaxRetries {
            nextAttempt := attempts + 1
            delay := opts.WebhookRetryDelay * time.Duration(1<<(nextAttempt-1))
            fmt.Fprintf(os.Stderr, "\nWebhook failure (attempt %d/%d), retrying in %s...\n", nextAttempt, opts.WebhookMaxRetries, delay)
            time.AfterFunc(delay, func() {
                select {
                case a.hookChan <- parsedMessageJob{pm: pm, opts: opts, attempts: nextAttempt}:
                default:
                    fmt.Fprintln(os.Stderr, "Warning: Hook queue full, skipping retry.")
                }
            })
        } else {
            fmt.Fprintf(os.Stderr, "\nWebhook failed after %d attempts.\n", attempts+1)
        }
        if resp != nil {
            resp.Body.Close()
        }
        return
    }
    resp.Body.Close()
}
```

- [ ] **Step 5: Run test to verify pass**

Run: `/usr/local/go/bin/go test ./internal/app/... -v -run "TestDispatchWebhook_Retry"`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/app/sync.go internal/app/sync_test.go
git commit -m "feat: implement async webhook retry with exponential backoff"
```

---

## Task 4: Add CLI Flags

**Files:**
- Modify: `cmd/wacli/sync.go`

- [ ] **Step 1: Add flags to `newSyncCmd`**

Modify `cmd/wacli/sync.go`:

```go
func newSyncCmd(flags *rootFlags) *cobra.Command {
    var webhookMaxRetries int
    var webhookRetryDelay time.Duration
    // ...
    cmd.Flags().IntVar(&webhookMaxRetries, "webhook-max-retries", 3, "maximum number of retry attempts for webhooks")
    cmd.Flags().DurationVar(&webhookRetryDelay, "webhook-retry-delay", 5*time.Second, "initial delay for webhook retries")
    // ... pass to SyncOptions ...
}
```

- [ ] **Step 2: Verify flags in help**

Run: `/usr/local/go/bin/go run ./cmd/wacli/ sync --help`
Expected: flags appear.

- [ ] **Step 3: Commit**

```bash
git add cmd/wacli/sync.go
git commit -m "feat: add --webhook-max-retries and --webhook-retry-delay flags"
```

---

## Task 5: Final Polish and Documentation

- [ ] **Step 1: Update `CHANGELOG.md`**

Add entry under 0.5.0.

- [ ] **Step 2: Update `README.md`**

Add examples for retry flags.

- [ ] **Step 3: Run all tests**

Run: `/usr/local/go/bin/go test ./...`
Expected: ALL PASS

- [ ] **Step 4: Commit**

```bash
git add CHANGELOG.md README.md
git commit -m "docs: document webhook retry features"
```

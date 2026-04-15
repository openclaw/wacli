# wacli Webhook Robustness — Async Retry Design

**Date:** 2026-04-15  
**Goal:** Improve webhook delivery reliability by reusing HTTP clients and implementing asynchronous retries with exponential backoff.

---

## 1. Internal Changes

### 1.1 Shared HTTP Client (`internal/app/app.go`)

**Problem:** `dispatchHooks` creates a new `http.Client` for every single message, which is inefficient and doesn't take advantage of connection pooling (Keep-Alive).

**Fix:** Add `httpClient *http.Client` to the `App` struct. Initialize it in `New` with a reasonable default timeout (15s).

### 1.2 Async Retry Logic (`internal/app/sync.go`)

**Problem:** Webhook failures are currently permanent and silent.

**Fix:**
1. Introduce `webhookJob` struct to hold message, options, and attempt count.
2. Update `dispatchHooks` to use the shared client.
3. If a webhook fails (network error or status >= 400), and `Attempts < MaxRetries`:
   - Calculate backoff delay: `baseDelay * (2 ^ attempt)`.
   - Use `time.AfterFunc` to re-enqueue the job into `a.hookChan` or a dedicated `retryChan` after the delay.
4. If `Attempts == MaxRetries`, log the final failure to stderr.

### 1.3 Ordered vs. Unordered
This implementation is **unordered**. If message A fails and message B succeeds immediately after, B will arrive at the webhook before A's retry. This is acceptable for the requested "Async Retry" behavior.

---

## 2. CLI Interface

### 2.1 New Flags (`cmd/wacli/sync.go`)

- `--webhook-max-retries int` (default `3`): Maximum number of retry attempts after the first failure.
- `--webhook-retry-delay duration` (default `5s`): Initial delay for the first retry.

---

## 3. Data Flow

1. **Sync Loop**: Receives message -> sends to `a.hookChan`.
2. **Hook Worker**: 
   - Pops from `a.hookChan`.
   - Calls `a.dispatchWebhook(job)`.
3. **Dispatch Webhook**:
   - Computes HMAC (if secret set).
   - POSTs via `a.httpClient`.
   - **Success**: done.
   - **Failure**: 
     - Checks `job.Attempts < MaxRetries`.
     - Schedules `time.AfterFunc(delay, func() { a.hookChan <- job })`.
     - Logs attempt to stderr.

---

## 4. Test Plan

### 4.1 `internal/app/sync_test.go` extensions

| Test | What it verifies |
|------|-----------------|
| `TestDispatchWebhook_Retry` | Server returns 500 twice, then 200. Verify the message is eventually delivered and 3 total attempts were made. |
| `TestDispatchWebhook_MaxRetries` | Server always returns 500. Verify it stops after the max attempts. |
| `TestDispatchWebhook_SharedClient` | (Internal) Verify multiple calls use the same client instance. |

---

## 5. What is NOT in scope

- Persistence: Retries are lost if the process exits.
- Ordered retries: A retry might be delivered after a newer message.
- Per-command timeout configuration (global timeout used).

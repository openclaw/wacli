# wacli Contribution — Balanced Improvements Design

**Date:** 2026-04-14  
**Approach:** B — Equilibrada  
**Scope:** Bug fixes + test coverage + two new features (HMAC webhook, flexible export format)

---

## 1. Bug Fixes

### 1.1 YAML Injection Fix (`internal/out/obsidian.go`)

**Problem:** `fmt.Fprintf(w, "%q", value)` uses Go string quoting, which produces escape sequences (`\n`, `\t`) that are invalid inside YAML double-quoted strings. A group name containing a newline or backslash will corrupt the frontmatter.

**Fix:** Introduce a local `yamlQuote(s string) string` helper that:
1. Replaces `\n` and `\r` with a space
2. Escapes `"` as `\"`
3. Wraps the result in double quotes

Apply to all string fields in the YAML header: `projeto`, `tags`.

**Files:** `internal/out/obsidian.go`

### 1.2 Time Filters for Export (`cmd/wacli/messages.go`)

**Problem:** `messages export` lacks `--after` and `--before` flags present in `messages list` and `messages search`.

**Fix:** Add `--after string` and `--before string` flags to `newMessagesExportCmd`, using the existing `parseTime()` helper and passing `*time.Time` pointers to `store.ListMessagesParams` — identical to the pattern in `newMessagesListCmd`.

**Files:** `cmd/wacli/messages.go`

### 1.3 Configurable Hook Worker Count (`cmd/wacli/sync.go`)

**Problem:** Worker count is hardcoded as `4` in `sync.go:43`.

**Fix:** Add `--hook-workers int` flag (default `4`). Pass the value to `a.StartHookWorkers(ctx, hookWorkers)`.

**Files:** `cmd/wacli/sync.go`

---

## 2. New Features

### 2.1 HMAC-SHA256 Webhook Signature

**Purpose:** Allow webhook consumers (n8n, Make, custom endpoints) to verify that payloads originate from wacli and have not been tampered with. Follows the same convention as GitHub Webhooks (`X-Wacli-Signature: sha256=<hex>`).

**Interface:**
```
wacli sync --webhook "https://n8n.example.com/hook" \
           --webhook-secret "my-secret"
```

**Data flow:**
1. New flag `--webhook-secret string` added to `newSyncCmd`
2. Field `WebhookSecret string` added to `SyncOptions`
3. In `dispatchHooks`, when `opts.WebhookSecret != ""`:
   - Compute `hmac.New(sha256.New, []byte(secret))` over the JSON payload
   - Set header `X-Wacli-Signature: sha256=<hex.EncodeToString(mac.Sum(nil))>`
4. When secret is empty, header is omitted — zero overhead

**Files:** `cmd/wacli/sync.go`, `internal/app/sync.go`

### 2.2 Flexible Export Format

**Purpose:** Make `messages export` consistent with the rest of the CLI and enable machine-to-machine use (AI agents reading JSON) alongside human/Obsidian use (Markdown).

**Flags:**
- `--json` (global flag, already exists via `flags.asJSON`): outputs `{"messages": msgs, "fts": a.DB().HasFTS()}` wrapped in the standard `{"success":true,"data":{...}}` envelope via `out.WriteJSON` — matches the shape of `messages list --json`
- `--format obsidian|plain-md` (default: `obsidian`): selects the Markdown variant
  - `obsidian`: current behavior — YAML frontmatter + dated sections
  - `plain-md`: no YAML block, just the `# ChatName` heading and dated sections

**Precedence:** `--json` takes priority over `--format`. If both are set, JSON wins.

**New function:** `WritePlainMarkdown(w io.Writer, chatName string, messages []store.Message) error` in `internal/out/obsidian.go`. Reuses the same message-rendering loop, skips the `---` frontmatter block.

**Files:** `cmd/wacli/messages.go`, `internal/out/obsidian.go`

---

## 3. Tests

### 3.1 `internal/out/obsidian_test.go` (new file)

| Test | What it verifies |
|------|-----------------|
| `TestWriteObsidianMarkdown_YAMLFrontmatter` | Output contains `---`, required YAML keys, and that `"` / newline in chat name does not break structure |
| `TestWriteObsidianMarkdown_ChronologicalOrder` | Messages with reversed input are written oldest-first |
| `TestWriteObsidianMarkdown_MediaMessages` | Media-only messages render as `_Sent image_` etc. |
| `TestWritePlainMarkdown_NoFrontmatter` | Output does not contain `---` block |
| `TestWritePlainMarkdown_SameContent` | Body content identical to obsidian variant minus frontmatter |

### 3.2 `internal/app/sync_test.go` (extensions)

| Test | What it verifies |
|------|-----------------|
| `TestDispatchHooks_Exec` | `--exec` writes message JSON to a temp file via shell; file exists and is valid JSON after dispatch |
| `TestDispatchHooks_Webhook` | `httptest.NewServer` receives POST with `Content-Type: application/json` and valid message payload |
| `TestDispatchHooks_WebhookHMAC` | Same server receives `X-Wacli-Signature` header; value matches independently computed HMAC-SHA256 |
| `TestDispatchHooks_WebhookTimeout` | Server that blocks forever + context cancelled immediately; dispatch returns without panic and without hanging goroutines |

### 3.3 Out of scope

Unit tests for the worker pool goroutines (`StartHookWorkers`) — covered indirectly by the dispatch tests above.

---

## 4. File Change Summary

| File | Type | What changes |
|------|------|-------------|
| `internal/out/obsidian.go` | Modified | `yamlQuote` helper, `WritePlainMarkdown` function |
| `internal/out/obsidian_test.go` | New | 5 tests for formatter |
| `cmd/wacli/messages.go` | Modified | `--after`/`--before`/`--format` flags + `--json` support in export |
| `cmd/wacli/sync.go` | Modified | `--webhook-secret` and `--hook-workers` flags |
| `internal/app/sync.go` | Modified | HMAC computation in `dispatchHooks`, `WebhookSecret` in `SyncOptions` |
| `internal/app/sync_test.go` | Modified | 4 new hook dispatch tests |

---

## 5. What is NOT in scope

- Webhook retry/backoff (separate PR, higher complexity)
- Export to multiple files (one file per chat)
- `--format json` alias (redundant with global `--json`)
- Changes to history sync, media download, or auth flows

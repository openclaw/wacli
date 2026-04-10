# CLAUDE.md

Go CLI for WhatsApp via whatsmeow. Syncs messages to local SQLite (FTS5), offline search, send/receive, group management.

## Build / Test

```bash
pnpm build                # => dist/wacli (CGO required — sqlite3)
pnpm test                 # standard + FTS5-tagged tests
pnpm test:go              # without FTS5
pnpm test:fts             # with -tags sqlite_fts5
pnpm lint                 # go vet
pnpm format               # gofmt -w .
pnpm format:check         # CI gate
```

Single test: `go test -run TestName ./internal/store/...`

CGO is mandatory (`mattn/go-sqlite3`). GCC 15+: `CGO_CFLAGS` workaround applied in `package.json` build script.

## Architecture

```
cmd/wacli/        Cobra commands — one file per command group
internal/
  app/            App struct = central coordinator; holds WAClient + store.DB
  wa/             whatsmeow wrapper; WAClient interface enables mock testing
  store/          SQLite (WAL mode, FTS5); schema in migrations.go
  out/            Output: human tables (default) or JSON envelope (--json)
  lock/           Exclusive file lock — single instance per store dir
  config/         Config management
  pathutil/       Path sanitization
```

**Two SQLite DBs** in `~/.wacli/` (override: `--store DIR`):
- `session.db` — whatsmeow-owned (device identity, keys)
- `wacli.db` — app-owned (messages, chats, contacts, groups, FTS5)

**Data flow:** CLI command → `App` method → `WAClient` (network) + `store.DB` (persistence)

## Key Patterns

- `WAClient` interface (`internal/app/app.go`) abstracts whatsmeow for testability
- Tests: standard Go table-driven; DB tests use `openTestDB()` with `t.TempDir()`
- Store locking: every command acquires `LOCK` file; concurrent access fails fast with PID
- Output: always stdout for data, stderr for logs/progress
- FTS5: standalone virtual table `messages_fts` synced via triggers; LIKE fallback when FTS unavailable

## Env Vars

- `WACLI_DEVICE_LABEL` — linked device label in WhatsApp
- `WACLI_DEVICE_PLATFORM` — platform override (default: CHROME)

## Release

Tag `vX.Y.Z` → GitHub Actions → GoReleaser (macOS universal + Linux amd64/arm64 + Windows). Homebrew tap updated manually after.

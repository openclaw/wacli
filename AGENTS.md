# Repository Guidelines

## Project Structure
- `cmd/wacli/`: CLI command wiring (auth, sync, messages, send, media, contacts, chats, groups, history, presence, doctor).
- `internal/app/`: core app, whatsmeow event handling, backfill, sync idle logic.
- `internal/store/`: SQLite schema, migrations, FTS5 search, chats/contacts/groups/messages/media queries.
- `internal/wa/`: whatsmeow client wrapper, JID resolution, message parsing (text, business, media, context).
- `internal/config/`: store-dir resolution (`WACLI_STORE_DIR` env → XDG state dir on Linux → `~/.wacli`).
- `internal/lock/`: platform-specific LOCK-file locking; acquired before all write commands.
- `internal/out/`: JSON + table output helpers; all human text goes through here.
- `internal/fsutil/`: enforces 0700/0600 owner-only permissions on store files.
- `internal/pathutil/`: sanitises StorePath; rejects `?` and `#` to prevent URI injection.
- `internal/sqliteutil/`: sqlite file helpers.
- Tests sit next to the code they cover (`*_test.go`).

## Key Architectural Facts
- **Two databases**: `session.db` (managed by whatsmeow) + `wacli.db` (app data, FTS5 search).
- **FTS5 table** is a separate trigger-synced table in `wacli.db`; requires `-tags sqlite_fts5` at build time.
- **Store lock**: a `LOCK` file in the store dir is acquired before any write operation; `--lock-wait` controls the wait timeout.
- **Read-only mode**: `--read-only` flag or `WACLI_READONLY=1` env; write commands exit immediately with a clear error.
- **Send retry**: bounded 45 s attempt timeout; retries once after reconnect for stale-session / usync-timeout errors.
- **Store path precedence**: `--store` flag → `WACLI_STORE_DIR` env → XDG `~/.local/state/wacli` on Linux (legacy `~/.wacli` fallback) → `~/.wacli` elsewhere.

## Build, Test, and Development Commands
- Build: `pnpm build` — compiles with `-tags sqlite_fts5` and `CGO_CFLAGS=-Wno-error=missing-braces` (required for GCC 15+).
- Run: `pnpm wacli -- <args>` — rebuilds then runs.
- Test: `pnpm test` — runs `go test ./...` (plain), `go test -tags sqlite_fts5 ./...` (FTS), and a Windows lock cross-compile check.
- Lint: `pnpm lint` — `go vet ./...`.
- Format fix: `pnpm format` — `gofmt -w .`.
- Format check: `pnpm format:check` — fails if any file would change.
- **Full gate** (must pass before every PR): `pnpm format:check && pnpm lint && pnpm test && pnpm build && git diff --check`.

## Coding Style
- Standard `gofmt` formatting; run `pnpm format` before committing.
- Output: send structured data to stdout (`--json` / table); send human hints, progress, and errors to stderr via `internal/out`.
- Prefer explicit error returns over panics; write short, early-return functions.
- No build-time CGO beyond sqlite3; keep the dependency tree minimal.

## Testing Guidelines
- Every bug fix should ship with a regression test.
- FTS-sensitive tests must run under `-tags sqlite_fts5`; non-FTS path tests must also pass without the tag.
- Use `fake_wa_test.go` / table-driven tests for whatsmeow interaction; avoid hitting real WhatsApp in unit tests.
- Integration tests that need a live account are opt-in and not part of the standard gate.

## Commit & Pull Request Guidelines
- Follow Conventional Commits: `feat:`, `fix:`, `docs:`, `refactor:`, `test:`, `security:`, `ci:` with an imperative summary.
- Keep commits focused; avoid bundling unrelated changes.
- PRs should state: what changed, why, how it was tested, and any new flags or env vars.
- Run the full gate locally before opening a PR; CI runs the same commands.
- New contributors: add `Co-authored-by:` trailers when building on their work.

## Agent Notes
- This repo uses `AGENTS.md` as its agent-instruction source; `CLAUDE.md` is explicitly ignored.
- For agent-safe execution, pass `--read-only` (or set `WACLI_READONLY=1`) to prevent writes.
- Prefer `--json` output for machine-readable parsing.
- Do not add dependencies or change build tooling without confirming with the maintainer.

# Changelog

## 0.6.1 - Unreleased

### Security

- Auth: reject `?` and `#` in whatsmeow session store paths to avoid SQLite URI parameter injection. (#180 — thanks @shaun0927)

### Fixed

- Media: recover panics per download job so one bad payload no longer drains the worker pool. (#179 — thanks @shaun0927)
- Messages: show display text for replies, reactions, and media in `messages context`. (#183 — thanks @fuleinist)
- Send: strip a leading `+` from phone-number recipients before building WhatsApp JIDs. (#74 — thanks @FrederickStempfle)
- Search: keep FTS5 enabled after reopening existing databases with already-applied migrations. (#185 — thanks @iamhitarth)
- Send: persist retry-message plaintext so linked devices can decrypt retried messages. (#186 — thanks @SimDamDev)
- Sync: start `sync --once` idle timing after the `Connected` event. (#171 — thanks @fuleinist)
- Sync: include event type, stack trace, and recovery count when logging recovered event-handler panics. (#181 — thanks @shaun0927)

### Docs

- Maintainers: add CODEOWNERS and maintainer contact info.

### Chore

- Dependencies: update Go modules including `whatsmeow`, `go-sqlite3`, `x/*`, and related runtime libs.

## 0.6.0 - 2026-04-14

### Security

- Search: sanitize FTS5 user queries and escape LIKE wildcards to avoid query-syntax injection.
- Store: reject SQLite URI path injection via `?` and `#`, guard empty table names, and strip null/control chars from sanitized paths.
- Sync: recover panics in event handlers and media workers instead of crashing the process.

### Fixed

- Sync: bound reconnect duration so long-running commands do not hold the store lock forever.
- CLI: force exit on a second SIGINT during long-running commands.

### Added

- Store: add `WACLI_STORE_DIR` to configure the default store directory.

### Chore

- Dependencies: bump `filippo.io/edwards25519`.

## 0.5.0 - 2026-04-12

### Fixed

- WhatsApp connectivity: update `whatsmeow` for the current WhatsApp protocol and fix `405 (Client Outdated)` failures.

### Changed

- Internal architecture: split store and groups command logic into focused modules for cleaner maintenance and safer follow-up changes.
- Dependencies: bump core Go modules including `whatsmeow`, `go-sqlite3`, and `x/*` runtime libs.

### Build

- CI: extract a shared setup action and reuse it across CI and release workflows.
- Release: install arm64 libc headers in release workflow to improve ARM build reliability.

### Docs

- README: update usage/docs for the 0.2.0 release baseline.
- Changelog: sync unreleased notes with all commits since `v0.2.0`.

### Chore

- Version: bump CLI version string to `0.5.0`.

## 0.2.0 - 2026-01-23

### Added

- Messages: store display text for reactions, replies, and media; include in search output.
- Send: `wacli send file --filename` to override display name for uploads. (#7 — thanks @plattenschieber)
- Auth: allow `WACLI_DEVICE_LABEL` and `WACLI_DEVICE_PLATFORM` overrides for linked device identity. (#4 — thanks @zats)

### Fixed

- Build: preserve existing `CGO_CFLAGS` when adding GCC 15+ workaround. (#8 — thanks @ramarivera)
- Messages: keep captions in list/search output.

### Build

- Release: multi-OS GoReleaser configs and workflow for macOS, linux, and windows artifacts.

### Docs

- Install: clarify Homebrew vs local build paths.
- Changelog: introduce project changelog and prep `0.2.0` release notes.

## 0.1.1 - 2025-12-12

### Fixed

- Release: fix workflow for CGO builds.

## 0.1.0 - 2025-12-12

### Added

- Auth: `wacli auth` QR login, bootstrap sync, optional follow, idle-exit, background media download, contacts/groups refresh.
- Sync: non-interactive `wacli sync` once/follow, never shows QR, idle-exit, background media download, optional contacts/groups refresh.
- Messages: list/search/show/context with chat/sender/time/media filters; FTS5 search with LIKE fallback and snippets.
- Send: text and file (image/video/audio/document) with caption and MIME override.
- Media: download by chat/id, resolves output paths, and records downloaded media in the DB.
- History: on-demand backfill per chat with request count, wait, and idle-exit.
- Contacts: search/show; import from WhatsApp store; local alias and tag management.
- Chats: list/show with kind and last message timestamp.
- Groups: list/refresh/info/rename; participants add/remove/promote/demote; invite link get/revoke; join/leave.
- Diagnostics: `wacli doctor` for store path, lock status/info, auth/connection check, and FTS status.
- CLI UX: human-readable output by default with `--json`, global `--store`/`--timeout`, plus `wacli version`.
- Storage: default `~/.wacli`, lock file for single-instance safety, SQLite DB with FTS5, WhatsApp session store, and media directory.

# Changelog

## 0.5.0 - Unreleased

### Changed

- Internal architecture: split store and groups command logic into focused modules for cleaner maintenance and safer follow-up changes.

### Build

- CI: extract a shared setup action and reuse it across CI and release workflows.
- Release: install arm64 libc headers in release workflow to improve ARM build reliability.

### Docs

- README: update usage/docs for the 0.2.0 release baseline.
- Changelog: roll unreleased tracking from `0.2.1` to `0.5.0`.

### Chore

- Version: bump CLI version string to `0.5.0` (unreleased).

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

## 0.1.0 - 2026-01-01

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

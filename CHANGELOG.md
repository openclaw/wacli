# Changelog

## 0.7.0 - Unreleased

### Added

- CLI: add `--read-only`/`WACLI_READONLY` to reject commands that write WhatsApp or the local store.
- CLI: add `--lock-wait` to wait for transient store locks before failing write commands.
- CLI: add `--full` to disable table truncation; piped output now keeps full message IDs. (#13 — thanks @rickhallett)
- CLI: add `presence typing` and `presence paused` commands for WhatsApp composing indicators. (#76 — thanks @redemerco)
- Diagnostics: show linked JID and local store counts in `auth status` and `doctor`. (#149 — thanks @draix)
- Messages: add `messages list --sender`, `--from-me`, `--from-them`, and `--asc` filters. (#153 — thanks @draix)
- Messages: add `messages search --has-media`, `--type text`, case-insensitive media types, and validation for contradictory filters. (#128 — thanks @ImLukeF and @Mansehej)
- Messages: extract searchable/display text from WhatsApp Business templates, buttons, interactive messages, and list replies. (#79 — thanks @terry-li-hm)
- Auth: add `auth --qr-format text` to print the raw WhatsApp QR payload for external renderers. (#22 — thanks @teren-papercutlabs)
- Auth: add `auth --phone` for WhatsApp's phone-number pairing flow on headless systems. (#148, #184 — thanks @giovanninibarbosa and @KillerSnails)
- Auth: auto-detect a readable linked-device label and default linked-device platform to desktop. (#100 — thanks @pmatheus)
- Send: add `send react` to add or clear reactions, with group sender validation. (#151 — thanks @draix)
- Send: add `send file --reply-to` for quoted media/document replies. (#68 — thanks @vlassance)
- Send: accept common phone-number formatting in recipient flags while still storing digits-only WhatsApp JIDs. (#130 — thanks @fahmidme and @ImLukeF)
- Send: resolve `send text/file --to` against local contacts, groups, and chats, with `--pick` for non-interactive disambiguation. (#122 — thanks @AndroidPoet)

### Security

- Auth: reject `?` and `#` in whatsmeow session store paths to avoid SQLite URI parameter injection. (#180 — thanks @shaun0927)
- Media: reject send-file uploads and media downloads larger than 100 MiB before reading or writing the payload. (#63 — thanks @alexander-morris)
- Send: validate phone-number recipients before constructing WhatsApp JIDs. (#144 — thanks @draix)
- Store: restrict index and session SQLite database files to owner-only permissions. (#147 — thanks @draix)

### Fixed

- Auth: propagate QR channel setup errors and surface actionable QR pairing failures. (#100 — thanks @pmatheus)
- Build: fail cgo-disabled CLI builds at compile time instead of shipping a go-sqlite3 stub binary. (#194 — thanks @rajgopalv)
- Chats: resolve mapped historical `@lid` chat rows in `chats list/show` output. (#31, #89 — thanks @bhaskoro-muthohar and @alexph-dev)
- Groups: hide groups after `groups leave`, mark missing joined groups as left during refresh, and show them again if a later refresh reports membership. (#125, #129 — thanks @SeifBenayed and @ImLukeF)
- History: cap on-demand backfill at 500 messages per request and 100 requests per run.
- History: skip automatic initial history-sync blob downloads during on-demand backfill to avoid OOM on constrained Linux/ARM devices. (#84 — thanks @jyothepro)
- Messages: normalize device-specific `@s.whatsapp.net` JIDs before storing chats, contacts, and senders.
- Messages: include mapped `@lid` rows when listing, searching, showing, or contextualizing by phone-number chat JID.
- Messages: read stored sender names back from SQLite and resolve blank historical `@lid` senders at display time.
- Messages: make `messages show` prefer stored display text and include stored media/download details.
- Messages: store structured reaction target IDs and emoji in SQLite. (#67 — thanks @vlassance)
- Messages: store forwarded-message metadata and add `--forwarded` filters for list/search. (#24 — thanks @bnvyas)
- Doctor: report lock owner PID and distinguish paired stores locked by another process. (#105 — thanks @artemgetmann)
- Media: recover panics per download job so one bad payload no longer drains the worker pool. (#179 — thanks @shaun0927)
- Messages: attribute history messages from LID-addressed groups to the top-level participant sender. (#19 — thanks @entropyy0)
- Messages: show display text for replies, reactions, and media in `messages context`. (#183 — thanks @fuleinist)
- Send: strip a leading `+` from phone-number recipients before building WhatsApp JIDs. (#74 — thanks @FrederickStempfle)
- Search: keep FTS5 enabled after reopening existing databases with already-applied migrations. (#185 — thanks @iamhitarth)
- Send: add `send text --reply-to` for quoted replies, with sender inference for synced group messages. (#154 — thanks @draix)
- Send: bound send attempts and reconnect once for stale-session/time-out failures instead of hanging indefinitely. (#115 — thanks @0xatrilla)
- Send: include the Opus codec parameter when sending OGG audio so WhatsApp delivers it as audio. (#41 — thanks @emre6943)
- Send: persist retry-message plaintext so linked devices can decrypt retried messages. (#186 — thanks @SimDamDev)
- Store: use the XDG state directory on Linux by default, while keeping existing `~/.wacli` stores working. (#172, #164 — thanks @txhno)
- Sync: guard lazy WhatsApp client initialization against concurrent `OpenWA` calls. (#62 — thanks @thakoreh)
- Sync: decrypt encrypted reactions delivered through history sync before storing them. (#192 — thanks @matrixise)
- Sync: resolve live `@lid` chat and sender JIDs to phone-number JIDs before storing messages. (#196 — thanks @mahidconseil)
- Sync: warn when encrypted reaction messages cannot be decrypted instead of dropping the failure silently. (#192 — thanks @matrixise and @dinakars777)
- Sync: keep `sync --once` idle timing focused on message/history events so connection chatter cannot hang exit. (#119 — thanks @jyothepro)
- Sync: start `sync --once` idle timing after the `Connected` event. (#171 — thanks @fuleinist)
- Sync: include event type, stack trace, and recovery count when logging recovered event-handler panics. (#181 — thanks @shaun0927)
- Sync: apply bounded backpressure to media download enqueueing instead of spawning unbounded overflow goroutines. (#121 — thanks @jyothepro)
- Windows: split store locking by platform so the lock package compiles on Windows. (#188 — thanks @dinakars777)

### Docs

- README: add a documentation index and complete command quick reference.
- Docs: add an overview plus one page for every top-level CLI subcommand.
- Maintainers: add CODEOWNERS and maintainer contact info.
- Agents: add AGENTS.md for AI agent guidance. (#190 — thanks @adhitShet)

### Chore

- CI: compile-test the Windows lock package to catch platform regressions. (#188 — thanks @dinakars777)
- Dependencies: update Go modules including `whatsmeow`, `go-sqlite3`, `x/*`, and related runtime libs.
- Refactor: split WhatsApp message parsing into focused text, media, business, and context helpers.
- Refactor: inject clocks in app/store paths for deterministic tests.
- Version: bump CLI version string to `0.7.0`.

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

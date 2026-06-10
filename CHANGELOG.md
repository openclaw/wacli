# Changelog

## 0.11.1 - Unreleased

### Added

- Groups: add live admin commands for creating groups, setting descriptions, toggling announce-only and admin-only edits, and approving or rejecting join requests. (#265 - thanks @dovocoder)
- Groups: include resolved phone numbers in pending join request output when LID mappings are available. (#273 - thanks @cielecki)
- Profile: add commands to remove the profile picture, set About text, set the profile display name, fetch profile picture metadata, fetch a user's About text, and fetch WhatsApp Business profile details. (#267 - thanks @dovocoder)
- Sync: add opt-in keepalive-failure stale detection for `sync --follow` (`1s` to `<2m20s`), including forced reconnect, a `stale` NDJSON event, a private store `HEARTBEAT`, and `doctor --json` `last_activity_at`. (#278 - thanks @thedavidweng)

### Security

- Media: bound direct WhatsApp media downloads with a response-header timeout while preserving the caller's body download budget. (#271 - thanks @jason-allen-oneal)

### Fixed

- Media: download synced WhatsApp GIF playback videos by treating their stored `gif` label as video-encrypted media. (#274 - thanks @larskluge)
- Sync: reconnect after WhatsApp replaces the linked-device stream instead of leaving `sync --follow` offline. (#266 - thanks @ngutman)
- Sync: clear chat unread state from `ReceiptTypeReadSelf` receipts so linked-device reads still update when `regular_high` app-state sync is unhealthy. (#269 - thanks @p-jackson1)

## 0.11.0 - 2026-05-22

### Added

- Chats: store unread marker state and numeric `unread_count` separately; migrate existing stores away from sentinel unread values while preserving public chat JSON fields. (#255 - thanks @drelum and @dovocoder)
- Messages: add explicit `messages revoke` and `messages forward` commands for stored text and media/document messages. (#259 - thanks @dovocoder)
- Messages: persist quoted message ID and quoted sender JID metadata from WhatsApp reply context. (#260)
- Send: add `send select` to choose stored inbound WhatsApp quick-reply buttons and list rows from scripts. (#258 - thanks @morgs)

### Security

### Fixed

- Sync: store messages sent from other linked devices in the destination chat as outgoing messages.
- Calls: import call-log records from full history syncs and the `regular` app-state collection so old call events can be stored even when the live call signaling was missed. (#256)

### Docs

- Docs: update Homebrew install and release-token references to the OpenClaw Homebrew tap. (#254 - thanks @dovocoder)

## 0.10.0 - 2026-05-20

### Added

- Media: allow `media download --read-only` when `--output` is explicit, so synced media can be fetched without opening the WhatsApp session store or recording download state. (#250 - thanks @mbelinky)

### Security

### Fixed

- Release: publish target-specific macOS/Linux/Windows archives with one combined checksum file and update the OpenClaw Homebrew tap.
- Connect: send an `available` presence after authenticated connect so the server records the linked-device pushname; without it, downstream recipients receive `notify=""` and Cloud API verified-business webhooks silently drop the message at the gateway. (#252 - thanks @ceifa)

## 0.9.2 - 2026-05-17

### Added

- Send: add `wacli send status` for WhatsApp status broadcasts, including text statuses with optional background/font and media statuses with captions. (#247 - thanks @dovocoder)
- Store: persist synced and locally sent status broadcasts separately in `status_messages` instead of mixing them into normal chat messages.

### Security

- CI: pin GitHub Actions and Docker base images to immutable refs and pin GoReleaser to an exact version.
- Send: block automatic link previews from fetching localhost, private, link-local, multicast, and other non-public addresses.
- Sync: validate webhook URLs, redact webhook errors, disable private-network webhook targets by default, and add `--webhook-allow-private` for trusted local endpoints.

### Fixed

- Accounts: serialize account config mutations with a config lock and save through unique temporary files.
- CLI: strip terminal control characters from human/table/error output.
- CLI: open the local store through SQLite read-only mode for `--read-only` commands instead of initializing writer state.
- Media: enforce regular-file and size limits for sends, stickers, voice notes, profile pictures, contacts imports, thumbnails, and unknown-length downloads.
- Messages: make `--delete-media --for-me` remove the stored local media file when present.
- Store: count all chats and groups in `store stats` instead of the first 50 entries.
- Sync: warn when bootstrap contact/group/channel refresh fails instead of silently ignoring it.
- Sync: bound each webhook delivery request so shutdown is not stuck behind a slow endpoint.
- History: unwrap edited WhatsApp messages during history sync and backfill so stored/searchable text shows the edited body instead of `(message)`. (#246 - thanks @hiasinho)
- Polls: use WhatsApp's single-select poll creation field for outbound single-select polls and preserve unmatched poll vote hashes in `poll show --json`. (#248 - thanks @dovocoder)
- Sync: canonicalize `@lid` chat JIDs before enqueuing media downloads so `sync --follow --download-media` finds the correct DB row for live one-to-one messages. (#244 - thanks @Daniel1of1)

## 0.9.1 - 2026-05-15

### Added

- Calls: persist WhatsApp call signaling and call-log metadata, and add `wacli calls list`.

### Fixed

- Accounts: reject invalid account configs before saving and ignore relative `XDG_STATE_HOME` for default state paths.
- CLI: honor canceled store-lock waits before acquiring locks and stop reporting non-contention lock failures as ordinary contention.
- Media: fail before downloading when the output directory exists but is not writable.
- Media: sanitize `#`, control-wrapped blanks, and single-dot path components in generated media paths.
- Store: remove starred-message metadata when deleting chat-local data so cleanup cannot leave stale starred state behind.

## 0.9.0 - 2026-05-15

### Added

- Docker: add a local image with `/data` persistence, bundled `ffmpeg`, and Docker CI smoke coverage.
- Polls: add sending, voting, local result inspection, and sync persistence for WhatsApp polls. (#230 - thanks @Ortes)
- Send: add opt-in `send text --ephemeral` wrapping for disappearing-message chats. (#227 - thanks @AndroidPoet)

### Security

- Store: harden private-file writes and use static SQL for message reaction migrations. (#241 - thanks @cy701)

### Fixed

- Messages: preserve WhatsApp Business buttons and list options in JSON output. (#226 - thanks @ignaciovarela)
- Messages: extract WhatsApp NativeFlow interactive buttons from business messages. (#233 - thanks @ignaciovarela and @mturac)
- Send: canonicalize direct phone-number recipients before sending so WhatsApp accepts regional number rewrites. (#212, #240 - thanks @ceifa)
- Send: warm up recipients before send to reduce privacy-token failures. (#234 - thanks @AndroidPoet)

### Docs

- Docs: document named accounts in the quickstart and surface accounts, channels, store, and integrations pages in the docs navigation. (#235 - thanks @mamarchk)

### Chore

- Store: generate typed SQLite query wrappers with sqlc for stable store reads and writes.

## 0.8.1 - 2026-05-08

### Changed

- Module: migrate the canonical Go module/import path to `github.com/openclaw/wacli`. (#217 - thanks @dinakars777)
- Sync: collapse routine interactive TTY progress into a single updating status line while keeping warnings visible as normal stderr lines.

### Chore

- CI: make the Homebrew tap handoff use `openclaw/wacli` and skip gracefully when the tap token is missing. (#216 - thanks @dinakars777)
- Maintainers: remove the stale personal CODEOWNERS rule after the OpenClaw move. (#218 - thanks @dinakars777)
- Release: update GoReleaser archive config to the current v2 schema so release-config checks stay green.

### Fixed

- CLI: truncate table output by rune so emoji and other non-ASCII text stay valid UTF-8. (#222 - thanks @dinakars777)
- History: apply coverage/actionable filters before `LIMIT` so newer blocked chats do not hide ready chats. (#219 - thanks @dinakars777)
- Messages: extract display/search text from shared WhatsApp contact cards, including vCard phone numbers. (#214)
- Send: route whatsmeow diagnostics to stderr and clarify that `sent: true` means WhatsApp accepted the send request. (#215 - thanks @dinakars777)
- Sync: let explicit `--max-messages=0` override `WACLI_SYNC_MAX_MESSAGES`. (#220 - thanks @dinakars777)

## 0.8.0 - 2026-05-07

### Added

- Accounts: add first-class named WhatsApp accounts with isolated stores, `--account NAME`, and `wacli accounts list/add/use/show/remove`.

### Fixed

- Store: fix migration of legacy databases whose `groups` table existed before group hierarchy columns were introduced.

### Docs

- Docs: add a dedicated accounts page covering YAML config, store selection precedence, and multi-account usage.

## 0.7.0 - 2026-05-06

### Added

- CLI: add `--read-only`/`WACLI_READONLY` to reject commands that write WhatsApp or the local store.
- CLI: add `--lock-wait` to wait for transient store locks before failing write commands.
- CLI: add `--events` to emit machine-readable NDJSON lifecycle events for `auth`, `sync`, and `history backfill`. (#204 — thanks @dinakars777 and @0xatrilla)
- CLI: add `wacli docs` and root help text that point to the hosted docs at `https://wacli.sh`.
- CLI: add `--full` to disable table truncation; piped output now keeps full message IDs. (#13 — thanks @rickhallett)
- CLI: add `presence typing` and `presence paused` commands for WhatsApp composing indicators. (#76 — thanks @redemerco)
- Diagnostics: show linked JID and local store counts in `auth status` and `doctor`. (#149 — thanks @draix)
- Messages: add `messages list --sender`, `--from-me`, `--from-them`, and `--asc` filters. (#153 — thanks @draix)
- Messages: track WhatsApp starred state and add `messages starred` plus `--starred` filters for list/search. (#17 — thanks @dan-dr)
- Messages: track WhatsApp delete-for-me app-state events as local tombstones and add `messages delete --for-me`. (#64 — thanks @vlassance)
- Messages: add `messages edit` and `messages delete` for editing or revoking your own sent messages. (#80 — thanks @frapeti)
- Messages: add `messages search --has-media`, `--type text`, case-insensitive media types, and validation for contradictory filters. (#128 — thanks @ImLukeF and @Mansehej)
- Messages: add JSON export with `messages export --after` and `--before` filters.
- Messages: extract searchable/display text from WhatsApp Business templates, buttons, interactive messages, and list replies. (#79 — thanks @terry-li-hm)
- Contacts: add `contacts import-system` to import macOS Contacts display names as local metadata with alias-first precedence. (#33 — thanks @enki and @octaviofroid)
- Auth: add `auth --qr-format text` to print the raw WhatsApp QR payload for external renderers. (#22 — thanks @teren-papercutlabs)
- Auth: add `auth --phone` for WhatsApp's phone-number pairing flow on headless systems. (#148, #184 — thanks @giovanninibarbosa and @KillerSnails)
- Auth: auto-detect a readable linked-device label and default linked-device platform to desktop. (#100 — thanks @pmatheus)
- Chats: add archive/unarchive, pin/unpin, mute/unmute, and mark-read/mark-unread commands, plus list/show state fields. (#46 — thanks @decodiver22)
- Channels: add WhatsApp Channel list/info/join/leave commands, channel chat caching, and text/file sends to `...@newsletter` JIDs. (#72 — thanks @frapeti)
- Groups: persist WhatsApp Community parent/subgroup metadata from group refresh and info. (#207, #39 — thanks @dinakars777 and @TheMazzle)
- History: add `history coverage` and `history fill --dry-run` to inspect local archive anchors before running best-effort backfill. (#111 — thanks @cropsgg)
- Profile: add `profile set-picture` to update the authenticated account profile picture from JPEG or PNG input. (#198 — thanks @gado-ships-it)
- Sync: add signed live-message webhooks with `--webhook` and `--webhook-secret`. (#203 — thanks @dinakars777 and @Melostack)
- Send: add `send react` to add or clear reactions, with group sender validation. (#151 — thanks @draix)
- Send: add opt-in `send text --message-escapes` for `\n`, `\r`, `\t`, `\\`, and `\"` in `--message`. (#206 — thanks @slaveofcode)
- Send: add `send file --reply-to` for quoted media/document replies. (#68 — thanks @vlassance)
- Send: add repeatable `send text --mention` for WhatsApp user mentions in group messages. (#16 — thanks @nicozefrench and @sheepworrier)
- Send: add automatic link previews for text messages with `--no-preview` opt-out. (#94, #95 — thanks @elgatoflaco)
- Send: add `send sticker` for 512x512 WebP stickers, including animated-sticker metadata. (#205, #27 — thanks @dinakars777 and @fm1randa)
- Send: add `send voice` and `send file --ptt` for OGG/Opus WhatsApp voice notes. (#40, #41 — thanks @ricardopolo and @emre6943)
- Send: accept common phone-number formatting in recipient flags while still storing digits-only WhatsApp JIDs. (#130 — thanks @fahmidme and @ImLukeF)
- Send: resolve `send text/file --to` against local contacts, groups, and chats, with `--pick` for non-interactive disambiguation. (#122 — thanks @AndroidPoet)
- Store: add local-only `store stats`, `store cleanup`, `chats cleanup`, and `groups prune` commands with dry-run previews and confirmation gates. (#210, #211 — thanks @thedavidweng)

### Security

- Auth: reject `?` and `#` in whatsmeow session store paths to avoid SQLite URI parameter injection. (#180 — thanks @shaun0927)
- Media: reject send-file uploads and media downloads larger than 100 MiB before reading or writing the payload. (#63 — thanks @alexander-morris)
- Send: warn when send commands are invoked in rapid succession so automation rate-limit/account-risk is visible. (#53 — thanks @alexander-morris)
- Send: validate phone-number recipients before constructing WhatsApp JIDs. (#144 — thanks @draix)
- Sync: add message-count and database-size caps plus uncapped-sync warnings to avoid unbounded local history growth. (#54 — thanks @alexander-morris)
- Store: restrict index and session SQLite database files to owner-only permissions. (#147 — thanks @draix)

### Fixed

- Auth: retry transient websocket drops before QR or phone pairing completes.
- Auth: propagate QR channel setup errors and surface actionable QR pairing failures. (#100 — thanks @pmatheus)
- Build: fail cgo-disabled CLI builds at compile time instead of shipping a go-sqlite3 stub binary. (#194 — thanks @rajgopalv)
- Chats: resolve mapped historical `@lid` chat rows in `chats list/show` output. (#31, #89 — thanks @bhaskoro-muthohar and @alexph-dev)
- Groups: hide groups after `groups leave`, mark missing joined groups as left during refresh, and show them again if a later refresh reports membership. (#125, #129 — thanks @SeifBenayed and @ImLukeF)
- History: cap on-demand backfill at 500 messages per request and 100 requests per run.
- History: skip automatic initial history-sync blob downloads during on-demand backfill to avoid OOM on constrained Linux/ARM devices. (#84 — thanks @jyothepro)
- Messages: normalize device-specific `@s.whatsapp.net` JIDs before storing chats, contacts, and senders.
- Messages: include mapped `@lid` rows when listing, searching, showing, or contextualizing by phone-number chat JID.
- Messages: read stored sender names back from SQLite and resolve blank historical `@lid` senders at display time.
- Store: migrate historical `@lid` chat and message rows to mapped phone-number JIDs during authenticated startup. (#31, #89 — thanks @bhaskoro-muthohar, @alexph-dev, and @dinakars777)
- Messages: make `messages show` prefer stored display text and include stored media/download details.
- Messages: store structured reaction target IDs and emoji in SQLite. (#67 — thanks @vlassance)
- Messages: store forwarded-message metadata and add `--forwarded` filters for list/search. (#24 — thanks @bnvyas)
- Doctor: report lock owner PID and distinguish paired stores locked by another process. (#105 — thanks @artemgetmann)
- Media: recover panics per download job so one bad payload no longer drains the worker pool. (#179 — thanks @shaun0927)
- Media: allow explicit download outputs in shared directories like `/tmp` without trying to chmod the parent directory.
- Messages: attribute history messages from LID-addressed groups to the top-level participant sender. (#19 — thanks @entropyy0)
- Messages: show display text for replies, reactions, and media in `messages context`. (#183 — thanks @fuleinist)
- Send: strip a leading `+` from phone-number recipients before building WhatsApp JIDs. (#74 — thanks @FrederickStempfle)
- Search: keep FTS5 enabled after reopening existing databases with already-applied migrations. (#185 — thanks @iamhitarth)
- Send: delegate send commands through a running `sync --follow` process instead of failing on the store lock. (#6, #48, #92)
- Send: add `send text --reply-to` for quoted replies, with sender inference for synced group messages. (#154 — thanks @draix)
- Send: store outgoing `send react` messages locally so `messages list/show/search` can see the sent reaction immediately.
- Send: validate image uploads and include image dimensions plus a JPEG thumbnail for better client rendering.
- Send: keep the connection alive briefly after successful sends so retry receipts can repair first-send session gaps. (#89 — thanks @alexph-dev)
- Send: bound send attempts and reconnect once for stale-session/time-out failures instead of hanging indefinitely. (#115 — thanks @0xatrilla)
- Send: include the Opus codec parameter when sending OGG audio so WhatsApp delivers it as audio. (#41 — thanks @emre6943)
- Send: persist retry-message plaintext so linked devices can decrypt retried messages. (#186 — thanks @SimDamDev)
- Store: use the XDG state directory on Linux by default, while keeping existing `~/.wacli` stores working. (#172, #164 — thanks @txhno)
- Sync: guard lazy WhatsApp client initialization against concurrent `OpenWA` calls. (#62 — thanks @thakoreh)
- Sync: request a whatsmeow app-state recovery snapshot when LTHash verification fails. (#47 — thanks @elpargo)
- Sync: decrypt encrypted reactions delivered through history sync before storing them. (#192 — thanks @matrixise)
- Sync: resolve live `@lid` chat and sender JIDs to phone-number JIDs before storing messages. (#196 — thanks @mahidconseil)
- Sync: warn when encrypted reaction messages cannot be decrypted instead of dropping the failure silently. (#192 — thanks @matrixise and @dinakars777)
- CLI: emit command errors as NDJSON `error` events when `--events` is enabled.
- Sync: keep `sync --once` idle timing focused on message/history events so connection chatter cannot hang exit. (#119 — thanks @jyothepro)
- Sync: start `sync --once` idle timing after the `Connected` event. (#171 — thanks @fuleinist)
- Sync: include event type, stack trace, and recovery count when logging recovered event-handler panics. (#181 — thanks @shaun0927)
- Sync: apply bounded backpressure to media download enqueueing instead of spawning unbounded overflow goroutines. (#121 — thanks @jyothepro)
- Windows: split store locking by platform so the lock package compiles on Windows. (#188 — thanks @dinakars777)

### Docs

- README: add a documentation index and complete command quick reference.
- Docs: add an overview plus one page for every top-level CLI subcommand.
- Docs: add companion integration guidance for safe read-only SQLite, JSON, events, and webhook usage. (#71 — thanks @jaredtribe)
- Maintainers: add CODEOWNERS and maintainer contact info.
- Agents: add AGENTS.md for AI agent guidance. (#190 — thanks @adhitShet)

### Chore

- CI: compile-test the Windows lock package to catch platform regressions. (#188 — thanks @dinakars777)
- CLI: route `version` output through Cobra's configured output stream for easier command tests. (#78 — thanks @nikolasdehor)
- Dependencies: update Go modules including `whatsmeow`, `go-sqlite3`, `x/*`, and related runtime libs; refresh the pinned pnpm toolchain.
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

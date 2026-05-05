# wacli specification (plan)

Read when: changing the CLI contract, store model, sync model, or v1 behavior boundaries.

This document defines the v1 plan for `wacli`: a WhatsApp CLI that syncs messages locally, supports fast search, sending, and contact/group management. Implementation will use `whatsmeow` under the hood.

## Goals

- **Explicit authentication step**: `wacli auth` shows a QR code and completes login.
- **Auth starts syncing immediately**: after successful QR pairing, `wacli auth` begins initial sync (history + metadata).
- **Non-interactive sync**: `wacli sync` never displays a QR code; it fails with a clear error if not authenticated.
- **Fast offline message search**: local SQLite + FTS5 index.
- **Human-first output**: readable tables by default, `--json` opt-in for scripting.
- **Single-instance safety**: store locking to avoid multi-instance session conflicts (device/session replacement issues).
- **Group management**: list groups, inspect, rename, manage participants, invites.

## Non-goals (v1)

- Guaranteed full-history export (WhatsApp/WhatsApp Web history is best-effort).
- End-to-end “contact creation” in WhatsApp (we can manage local aliases/notes; WhatsApp contacts are sourced from the account/device).
- Full message-type parity (polls, reactions, ephemeral nuances, etc.) in v1.

## Terminology

- **JID**: WhatsApp Jabber ID, e.g. `1234567890@s.whatsapp.net` (user) or `123456789@g.us` (group).
- **Store directory**: directory containing all local state, default `~/.local/state/wacli` on Linux and `~/.wacli` elsewhere.

## Storage layout

Default store: `~/.local/state/wacli` on Linux and `~/.wacli` elsewhere (override with `--store DIR`). Existing Linux `~/.wacli` stores are reused when the XDG state store does not exist.

Proposed files:

- `<store>/session.db` — `whatsmeow` SQL store (device identity, keys, app-state).
- `<store>/wacli.db` — our SQLite DB (messages/chats, FTS, local metadata).
- `<store>/media/...` — downloaded media (optional, on-demand or background).
- `<store>/LOCK` — store lock to prevent concurrent access.

Rationale for two SQLite files: reduce coupling and keep the `whatsmeow`-owned schema separate from `wacli`’s local schema. It’s still “one store directory” for the user.

## Concurrency + locking

Every command that accesses the WhatsApp session must acquire an exclusive lock in the store dir.

Behavior:

- If lock is held: fail fast with a clear message (include PID and start time if available).
- This prevents running multiple `wacli` instances against the same WhatsApp device identity, which can cause disconnects or “device replaced” style failures.

## Authentication model

### Commands

- `wacli auth` (interactive)
  - If not authenticated: connect, show QR code, wait for success.
  - After success: start initial sync (bootstrap) immediately.
  - Exits after initial sync “goes idle” (configurable), unless `--follow` is set.

- `wacli sync` (non-interactive)
  - Requires an existing authenticated session in `session.db`.
  - Never displays QR; if not authenticated, prints “run `wacli auth`”.
  - `--once` performs a bounded sync and exits.
  - Default (or `--follow`) stays connected and continues capturing messages.

### UX principle

Only `wacli auth` is expected to show a QR code. `wacli sync` should be safe to run in scripts/daemons without surprising interactivity.

## Sync model (best-effort)

`wacli` captures messages via `whatsmeow` event handlers:

- `events.HistorySync`: initial/batch history sync delivered by WhatsApp Web.
- `events.Message`: new incoming/outgoing messages while connected.
- Connection lifecycle events (`Connected`, `Disconnected`) for logging/reconnect.

### Bootstrap sync (after auth)

Immediately after QR pairing success, `wacli auth` runs a bootstrap sync:

- Processes history sync events and stores message metadata.
- Updates chats, names, and contact-derived names as available.
- Optionally starts media download worker (off by default, behind a flag).
- Exits once “idle for N seconds” (no new history events) unless `--follow`.

### Continuous sync

`wacli sync --follow` keeps running:

- persists new messages as they arrive
- performs safe reconnect with backoff on disconnect
- continues best-effort history catch-up when WhatsApp emits it

## Database schema (wacli.db)

### Tables (proposed)

- `chats`
  - `jid` (PK), `name`, `kind` (`dm|group|broadcast`), `last_message_ts`, …
- `contacts`
  - `jid` (PK), `push_name`, `full_name`, `business_name`, `phone`, …
- `groups`
  - `jid` (PK), `name`, `owner_jid`, `created_ts`, …
- `messages`
  - `rowid` (PK), `chat_jid`, `msg_id`, `sender_jid`, `ts`, `from_me`, `text`, `media_type`, `media_caption`, `filename`, `mime_type`, `direct_path`, hashes/keys, …
  - unique constraint: (`chat_jid`, `msg_id`)
- `contact_aliases` (local management)
  - `jid` (PK/FK), `alias`, `notes`, `tags` (or join table)

### Message search (FTS5)

Use SQLite **FTS5** for fast full-text search.

Approach:

- Maintain canonical data in `messages`.
- Maintain an FTS5 virtual table `messages_fts` (external content) indexing:
  - message body text
  - media caption
  - document filename
  - (optionally) denormalized sender/chat names for convenience

Query behavior:

- Default: `MATCH` queries (FTS syntax) with ranking via `bm25`.
- Filters implemented in SQL: `--chat`, `--from`, `--after`, `--before`, `--has-media`, `--type`.
- Human output includes snippets/highlights; `--json` returns structured matches + offsets/snippet string.

Fallback:

- If FTS5 is unavailable, fall back to `LIKE` with an explicit warning (slower).

## CLI command surface (v1)

Global flags:

- `--store DIR` (default: XDG state dir on Linux, `~/.wacli` elsewhere)
- `--json` (default: human text)
- `--full` (disable table truncation; non-TTY output keeps full IDs)
- `--timeout DURATION` (non-sync commands; e.g. `5m`)
- `--lock-wait DURATION` (wait for the store lock before failing write commands)
- `--read-only` (reject commands that intentionally write WhatsApp or the local store; also `WACLI_READONLY=1`)
- `--version` (prints version and exits)

### Doctor

- `wacli doctor [--connect]`

### Auth

- `wacli auth [--follow] [--idle-exit 30s]`
- `wacli auth status`
- `wacli auth logout`

### Sync

- `wacli sync [--once] [--follow] [--download-media]`

Notes:

- `sync` errors if not authenticated (never prints QR).
- `--download-media` runs a bounded/concurrent media downloader for messages that contain downloadable media metadata.

### History backfill (best-effort)

WhatsApp Web history is best-effort. If you want to try fetching *older* messages for a specific chat, `wacli` can send an on-demand history request to your primary device:

- `wacli history backfill --chat JID [--count 50] [--requests N]`
- Backfill caps: `--count <= 500`, `--requests <= 100`.
- During backfill, automatic initial history-sync blob downloads are disabled; only on-demand history-sync notifications are downloaded and stored.

### Messages

- `wacli messages list [--chat JID] [--sender JID] [--from-me|--from-them] [--asc] [--limit N] [--before TS] [--after TS] [--forwarded] [--starred]`
- `wacli messages search <query> [--chat JID] [--from JID] [--limit N] [--before TS] [--after TS] [--type text|image|video|audio|document] [--forwarded] [--starred]`
- `wacli messages starred [--chat JID] [--limit N] [--before TS] [--after TS] [--asc]`
- `wacli messages export [--chat JID] [--limit N] [--before TS] [--after TS] [--output PATH]`
- `wacli messages show --chat JID --id MSG_ID`
- `wacli messages context --chat JID --id MSG_ID [--before N] [--after N]`

### Send

- `wacli send text --to RECIPIENT --message TEXT [--pick N] [--no-preview] [--reply-to MSG_ID] [--reply-to-sender JID]`
- `wacli send file --to RECIPIENT --file PATH [--caption TEXT] [--mime TYPE] [--pick N] [--ptt] [--reply-to MSG_ID] [--reply-to-sender JID]`
- `wacli send voice --to RECIPIENT --file PATH [--mime TYPE] [--pick N] [--reply-to MSG_ID] [--reply-to-sender JID]`
- `wacli send react --to PHONE_OR_JID --id MSG_ID [--reaction TEXT] [--sender JID]`

`RECIPIENT` accepts a JID, phone number, or synced contact/group/chat name. If a name is ambiguous, interactive terminals prompt; scripts can pass `--pick N`.
Text sends automatically include a link preview for the first `http://` or `https://` URL unless `--no-preview` is passed.
Voice notes require OGG/Opus audio and use optional `ffprobe`/`ffmpeg` metadata when available.

Send-file uploads and media downloads are capped at 100 MiB to avoid reading
or writing unexpectedly large payloads in one command.

### Contacts (read + local management)

- `wacli contacts search <query>`
- `wacli contacts show --jid JID`
- `wacli contacts refresh`
- `wacli contacts alias set --jid JID --alias "Name"`
- `wacli contacts alias rm --jid JID`
- `wacli contacts tags add|rm --jid JID --tag TAG`

### Chats

- `wacli chats list [--query TEXT]`
- `wacli chats show --jid JID`

### Groups

- `wacli groups list [--query TEXT]`
- `wacli groups refresh`
- `wacli groups info --jid GROUP_JID`
- `wacli groups rename --jid GROUP_JID --name "New Name"`
- `wacli groups participants add|remove --jid GROUP_JID --user PHONE_OR_JID [--user ...]`
- `wacli groups participants promote|demote --jid GROUP_JID --user PHONE_OR_JID [--user ...]`
- `wacli groups invite link get|revoke --jid GROUP_JID`
- `wacli groups join --code INVITE_CODE`
- `wacli groups leave --jid GROUP_JID`

## Output formats

Default: human-readable text (tables / aligned columns; TTY-aware wrapping).

Optional:

- `--json` prints `{"success":true,"data":...,"error":null}`-style responses.

Recommendation:

- Write logs/progress (sync counters, reconnect notices) to stderr.
- Write primary command output to stdout.

## Reliability considerations

- **Session conflicts**: running multiple instances can cause disconnects or “device replaced” behavior; locking is mandatory.
- **Reconnect**: on disconnect, retry with exponential backoff and respect context cancellation.
- **Idempotency**: message inserts are upserts keyed by (`chat_jid`, `msg_id`) so replays/history sync don’t duplicate data.

## Security considerations

- Store contains encryption keys/session data; protect permissions:
  - store dir `0700`
  - DB files `0600`
- Avoid printing sensitive identifiers in logs unless needed for debugging (`--verbose`).

## Implementation milestones

### v0.1 (MVP)

- `auth` (QR + bootstrap sync)
- `sync` (non-interactive, follow mode)
- `messages list/search` with FTS5
- `send text`
- store locking, default state dir

### v0.2

- contacts: show + local alias/notes/tags
- chats list/show with better naming resolution
- groups list/info/rename/participants

### v0.3

- media download command + optional background downloader
- `messages show/context` polish

## Prior art / credit

This spec borrows ideas and lessons learned from:

- `https://github.com/vicentereig/whatsapp-cli`

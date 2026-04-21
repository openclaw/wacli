# 🗃️ wacli — WhatsApp CLI: sync, search, send

WhatsApp CLI built on top of `whatsmeow`, focused on:

- Best-effort local sync of message history + continuous capture
- Fast offline search
- Sending text, quoted replies, and files
- Contact + group management
- Scriptable JSON output

This is a third-party tool that uses the WhatsApp Web protocol via `whatsmeow` and is not affiliated with WhatsApp.

## Status

Core implementation is in place. See `docs/spec.md` for design notes.

## Major features

- **Auth + sync**: `auth` shows QR login and bootstraps sync; `sync` is non-interactive, can run once or follow continuously, and can refresh contacts/groups.
- **Offline message store**: local SQLite store with FTS5 search when available and LIKE fallback.
- **Message tools**: list/search/show/context with chat, sender, direction, time, order, and media-type filters.
- **Sending**: send text, quoted text replies, and image/video/audio/document files with captions, MIME override, and custom display filenames.
- **Media**: download synced message media on demand, or download in the background during auth/sync.
- **Contacts/chats/groups**: search/show contacts, local aliases/tags, list/show chats, refresh/list/info/rename groups, manage participants, invite links, join, and leave.
- **Presence**: send typing/paused indicators.
- **Diagnostics + safety**: `doctor`, store locks with lock-owner reporting, owner-only database permissions, panic recovery, reconnect bounds, and bounded media queue backpressure.
- **CLI UX**: human-readable tables by default; `--json` for scripts; `--full` to avoid truncation.

## Install / Build

Choose **one** of the following options.  
If you install via Homebrew, you can skip the local build step.

### Option A: Install via Homebrew (tap)

- `brew install steipete/tap/wacli`

### Option B: Build locally

- `go build -tags sqlite_fts5 -o ./dist/wacli ./cmd/wacli`

Run (local build only):

- `./dist/wacli --help`

## Quick start

Default store directory is `~/.wacli`; override with `--store DIR` or `WACLI_STORE_DIR`.

```bash
# 1) Authenticate (shows QR), then bootstrap sync
pnpm wacli auth
# or, after building locally: ./dist/wacli auth

# 2) Keep syncing (never shows QR; requires prior auth)
pnpm wacli sync --follow

# Diagnostics
pnpm wacli doctor

# Search messages
pnpm wacli messages search "meeting"

# List recent messages from a chat, oldest first
pnpm wacli messages list --chat 1234567890@s.whatsapp.net --asc

# Show context around a message
pnpm wacli messages context --chat 1234567890@s.whatsapp.net --id <message-id>

# Backfill older messages for a chat (best-effort; requires your primary device online)
pnpm wacli history backfill --chat 1234567890@s.whatsapp.net --requests 10 --count 50

# Download media for a message (after syncing)
pnpm wacli media download --chat 1234567890@s.whatsapp.net --id <message-id>

# Send a message
pnpm wacli send text --to 1234567890 --message "hello"

# Send a quoted reply
pnpm wacli send text --to 1234567890 --message "replying" --reply-to <message-id>

# Send a file
pnpm wacli send file --to 1234567890 --file ./pic.jpg --caption "hi"
# Or override display name
pnpm wacli send file --to 1234567890 --file /tmp/abc123 --filename report.pdf

# List groups and manage them
pnpm wacli groups list
pnpm wacli groups rename --jid 123456789@g.us --name "New name"

# Send presence indicators
pnpm wacli presence typing --to 1234567890
pnpm wacli presence paused --to 1234567890
```

## High-level UX

- `wacli auth`: interactive login (shows QR code), then immediately performs initial data sync.
- `wacli sync`: non-interactive sync loop (never shows QR; errors if not authenticated).
- Output is human-readable by default; pass `--json` for machine-readable output.
- Pass `--full` to keep full IDs in table output; non-TTY output keeps full IDs automatically.

## Command surface

- `wacli auth [--follow] [--idle-exit 30s] [--download-media]`
- `wacli auth status`
- `wacli auth logout`
- `wacli sync [--once] [--follow] [--idle-exit 30s] [--max-reconnect 5m] [--download-media] [--refresh-contacts] [--refresh-groups]`
- `wacli messages list [--chat JID] [--sender JID] [--from-me|--from-them] [--asc] [--limit N] [--after DATE] [--before DATE]`
- `wacli messages search <query> [--chat JID] [--from JID] [--has-media] [--type text|image|video|audio|document]`
- `wacli messages show --chat JID --id MSG_ID`
- `wacli messages context --chat JID --id MSG_ID [--before N] [--after N]`
- `wacli send text --to PHONE_OR_JID --message TEXT [--reply-to MSG_ID] [--reply-to-sender JID]`
- `wacli send file --to PHONE_OR_JID --file PATH [--caption TEXT] [--filename NAME] [--mime TYPE]`
- `wacli media download --chat JID --id MSG_ID [--output PATH]`
- `wacli contacts search <query>`
- `wacli contacts show --jid JID`
- `wacli contacts refresh`
- `wacli contacts alias set|rm --jid JID [--alias NAME]`
- `wacli contacts tags add|rm --jid JID --tag TAG`
- `wacli chats list [--query TEXT] [--limit N]`
- `wacli chats show --jid JID`
- `wacli groups list [--query TEXT] [--limit N]`
- `wacli groups refresh`
- `wacli groups info --jid GROUP_JID`
- `wacli groups rename --jid GROUP_JID --name NAME`
- `wacli groups leave --jid GROUP_JID`
- `wacli groups participants add|remove|promote|demote --jid GROUP_JID --user PHONE_OR_JID`
- `wacli groups invite link get|revoke --jid GROUP_JID`
- `wacli groups join --code INVITE_CODE`
- `wacli history backfill --chat JID [--count 50] [--requests N]`
- `wacli presence typing --to PHONE_OR_JID [--media audio]`
- `wacli presence paused --to PHONE_OR_JID`
- `wacli doctor [--connect]`
- `wacli version`

## Storage

Defaults to `~/.wacli` (override with `--store DIR`).

## Environment overrides

- `WACLI_DEVICE_LABEL`: set the linked device label (shown in WhatsApp).
- `WACLI_DEVICE_PLATFORM`: override the linked device platform (defaults to `CHROME` if unset or invalid).
- `WACLI_STORE_DIR`: override the default store directory.

## Backfilling older history

`wacli sync` stores whatever WhatsApp Web sends opportunistically. To try to fetch *older* messages, use on-demand history sync requests to your **primary device** (your phone).

Important notes:

- This is **best-effort**: WhatsApp may not return full history.
- Your **primary device must be online**.
- Requests are **per chat** (DM or group). `wacli` uses the *oldest locally stored message* in that chat as the anchor.
- Recommended `--count` is `50` per request.

### Backfill one chat

```bash
pnpm wacli history backfill --chat 1234567890@s.whatsapp.net --requests 10 --count 50
```

### Backfill all chats (script)

This loops through chats already known in your local DB:

```bash
pnpm -s wacli -- --json chats list --limit 100000 \
  | jq -r '.[].JID' \
  | while read -r jid; do
      pnpm -s wacli -- history backfill --chat "$jid" --requests 3 --count 50
    done
```

## Prior art / credit

This project is heavily inspired by (and learns from) the excellent `whatsapp-cli` by Vicente Reig:

- [`whatsapp-cli`](https://github.com/vicentereig/whatsapp-cli)

## License

See `LICENSE`.

## Maintainers
- Created by [@steipete](https://github.com/steipete)
- Currently maintained by [@dinakars777](https://github.com/dinakars777)

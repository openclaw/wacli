# 🗃️ wacli — WhatsApp CLI: sync, search, send.

WhatsApp CLI built on top of `whatsmeow`, focused on:

- Best-effort local sync of message history + continuous capture
- Fast offline search
- Sending messages
- Contact + group management

This is a third-party tool that uses the WhatsApp Web protocol via `whatsmeow` and is not affiliated with WhatsApp.

## Status

Core implementation is in place. See `docs/spec.md` for the full design notes.

## Recent updates (0.2.0)

- Messages: search/list includes display text for reactions, replies, and media types.
- Send: `wacli send file --filename` to override the display name.
- Auth: optional `WACLI_DEVICE_LABEL` / `WACLI_DEVICE_PLATFORM` env overrides.

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

Default store directory is `~/.wacli` (override with `--store DIR`).

```bash
# 1) Authenticate (shows QR), then bootstrap sync
pnpm wacli auth
# or: ./dist/wacli auth (after pnpm build)

# 2) Keep syncing (never shows QR; requires prior auth)
pnpm wacli sync --follow

# Diagnostics
pnpm wacli doctor

# Search messages
pnpm wacli messages search "meeting"

# Backfill older messages for a chat (best-effort; requires your primary device online)
pnpm wacli history backfill --chat 1234567890@s.whatsapp.net --requests 10 --count 50

# Download media for a message (after syncing)
./wacli media download --chat 1234567890@s.whatsapp.net --id <message-id>

# Send a message
pnpm wacli send text --to 1234567890 --message "hello"

# Send a file
./wacli send file --to 1234567890 --file ./pic.jpg --caption "hi"
# Or override display name
./wacli send file --to 1234567890 --file /tmp/abc123 --filename report.pdf

# List groups and manage participants
pnpm wacli groups list
pnpm wacli groups rename --jid 123456789@g.us --name "New name"
```

## Prior Art / Credit

This project is heavily inspired by (and learns from) the excellent `whatsapp-cli` by Vicente Reig:

- [`whatsapp-cli`](https://github.com/vicentereig/whatsapp-cli)

## High-level UX

- `wacli auth`: interactive login (shows QR code), then immediately performs initial data sync.
- `wacli sync`: non-interactive sync loop (never shows QR; errors if not authenticated).
- Output is human-readable by default; pass `--json` for machine-readable output.

## Storage

Defaults to `~/.wacli` (override with `--store DIR`).

## Environment overrides

- `WACLI_STORE_DIR`: override the store directory (default: `~/.wacli`). Equivalent to `--store`.
- `WACLI_DEVICE_LABEL`: set the linked device label (shown in WhatsApp).
- `WACLI_DEVICE_PLATFORM`: override the linked device platform (defaults to `CHROME` if unset or invalid).

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

## License

See `LICENSE`.

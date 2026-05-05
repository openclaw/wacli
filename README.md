# 🗃️ wacli — WhatsApp CLI: sync, search, send

WhatsApp CLI built on top of `whatsmeow`, focused on:

- Best-effort local sync of message history + continuous capture
- Fast offline search
- Sending text, mentions, quoted replies, and files
- Contact + group management
- Scriptable JSON output

This is a third-party tool that uses the WhatsApp Web protocol via `whatsmeow` and is not affiliated with WhatsApp.

## Status

Core implementation is in place. The full documentation site lives at [wacli.sh](https://wacli.sh). Start with [docs/overview.md](docs/overview.md) for the command map and [docs/spec.md](docs/spec.md) for design notes.

## Documentation

Full docs site: <https://wacli.sh>.

- [Overview](docs/overview.md): store model, global flags, common flow, command index.
- [Auth](docs/auth.md): `auth`, `auth status`, `auth logout`.
- [Sync](docs/sync.md): `sync --once`, `sync --follow`, refresh, media download.
- [Messages](docs/messages.md): `messages list/search/starred/show/context`.
- [Send](docs/send.md): `send text/file/react`, recipient resolution, replies.
- [Media](docs/media.md): `media download`.
- [Contacts](docs/contacts.md): `contacts search/show/refresh`, aliases, tags.
- [Chats](docs/chats.md): `chats list/show`.
- [Groups](docs/groups.md): group list, refresh, info, rename, leave, participants, invites, join.
- [History](docs/history.md): `history backfill`.
- [Presence](docs/presence.md): `presence typing/paused`.
- [Profile](docs/profile.md): `profile set-picture`.
- [Doctor](docs/doctor.md): `doctor [--connect]`.
- [Version](docs/version.md): `version`, `--version`.
- [Completion](docs/completion.md): generated shell completions.
- [Help](docs/help.md): `help`, per-command `--help`.
- [Release](docs/release.md): release workflow and artifact expectations.

## Major features

- **Auth + sync**: `auth` shows QR login and bootstraps sync; `sync` is non-interactive, can run once or follow continuously, and can refresh contacts/groups.
- **Offline message store**: local SQLite store with FTS5 search when available and LIKE fallback.
- **Message tools**: list/search/show/context with chat, sender, direction, time, order, and media-type filters.
- **Sending**: send text, mentions, quoted replies, and image/video/audio/document files with captions, MIME override, and custom display filenames. Sends keep a short retry-receipt grace window, and rapid repeated sends warn on stderr.
- **Media**: download synced message media on demand, or download in the background during auth/sync; send-file uploads and downloads are capped at 100 MiB.
- **Contacts/chats/groups**: search/show contacts, local aliases/tags, list/show chats, refresh/list/info/rename groups, manage participants, invite links, join, and leave; left groups are hidden after leave.
- **Presence**: send typing/paused indicators.
- **Profile**: set the authenticated account profile picture from JPEG or PNG input.
- **Diagnostics + safety**: `doctor`, read-only mode, store locks with lock-owner reporting, lock waiting, owner-only database permissions, panic recovery, reconnect bounds, and bounded media queue backpressure.
- **CLI UX**: human-readable tables by default; `--json` for scripts; `--full` to avoid truncation.

## Install / Build

Choose **one** of the following options.  
If you install via Homebrew, you can skip the local build step.

### Option A: Install via Homebrew (tap)

- `brew install steipete/tap/wacli`

If a Linux install from the tap reports `Binary was compiled with 'CGO_ENABLED=0'`,
update the tap and rebuild the formula:

- `brew update`
- `brew reinstall steipete/tap/wacli`

### Option B: Build locally

`wacli` uses `go-sqlite3`, so local builds require cgo and a C compiler:

- macOS: Xcode Command Line Tools are enough.
- Debian/Ubuntu: `sudo apt install build-essential`

Build:

- `CGO_ENABLED=1 go build -tags sqlite_fts5 -o ./dist/wacli ./cmd/wacli`

Run (local build only):

- `./dist/wacli --help`

## Quick start

Default store directory is the XDG state directory on Linux (`~/.local/state/wacli`) and `~/.wacli` elsewhere. Existing Linux `~/.wacli` stores keep working; override with `--store DIR` or `WACLI_STORE_DIR`.

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

# Export messages to JSON with a time window
pnpm wacli messages export --chat 1234567890@s.whatsapp.net --after 2024-01-01 --before 2024-02-01 --output messages.json

# Backfill older messages for a chat (best-effort; requires your primary device online)
pnpm wacli history backfill --chat 1234567890@s.whatsapp.net --requests 10 --count 50

# Download media for a message (after syncing)
pnpm wacli media download --chat 1234567890@s.whatsapp.net --id <message-id>

# Send a message by phone/JID, or by a synced contact/group/chat name
pnpm wacli send text --to 1234567890 --message "hello"
# Link previews are added automatically for the first http(s) URL; use --no-preview to skip.
pnpm wacli send text --to 1234567890 --message "https://example.com" --no-preview
# Mention one or more users in a group text.
pnpm wacli send text --to "Family" --message "hey @15551234567" --mention +15551234567
# Phone numbers can also be passed as +E164 or formatted input like "+1 (234) 567-8900"
pnpm wacli send text --to mom --message "hello"
pnpm wacli send text --to "Family" --pick 2 --message "hello"

# Send a quoted reply
pnpm wacli send text --to 1234567890 --message "replying" --reply-to <message-id>

# Send a file
pnpm wacli send file --to 1234567890 --file ./pic.jpg --caption "hi"
# Send a quoted reply with a file
pnpm wacli send file --to 1234567890 --file ./pic.jpg --caption "replying" --reply-to <message-id>
# Or override display name
pnpm wacli send file --to 1234567890 --file /tmp/abc123 --filename report.pdf
# Send an OGG/Opus audio file as a native WhatsApp voice note
pnpm wacli send voice --to 1234567890 --file ./voice.ogg

# React to a message (omit --reaction for the default; use --reaction "" to clear)
pnpm wacli send react --to 1234567890 --id <message-id>

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
- `wacli sync` warns when local storage is uncapped; use `--max-messages` or `--max-db-size` to bound history growth.
- Output is human-readable by default; pass `--json` for machine-readable output.
- Pass `--full` to keep full IDs in table output; non-TTY output keeps full IDs automatically.
- Pass `--read-only` or set `WACLI_READONLY=1` to block commands that intentionally mutate WhatsApp or the local store.

## Command surface

Full command docs live under [docs/overview.md](docs/overview.md). Quick reference:

- `wacli auth [--follow] [--idle-exit 30s] [--download-media] [--qr-format terminal|text] [--phone PHONE]`
- `wacli auth status`
- `wacli auth logout`
- `wacli sync [--once] [--follow] [--idle-exit 30s] [--max-reconnect 5m] [--max-messages N] [--max-db-size SIZE] [--download-media] [--refresh-contacts] [--refresh-groups]`
- `wacli messages list [--chat JID] [--sender JID] [--from-me|--from-them] [--asc] [--limit N] [--after DATE] [--before DATE] [--forwarded] [--starred]`
- `wacli messages search <query> [--chat JID] [--from JID] [--has-media] [--type text|image|video|audio|document] [--forwarded] [--starred]`
- `wacli messages starred [--chat JID] [--limit N] [--after DATE] [--before DATE] [--asc]`
- `wacli messages export [--chat JID] [--limit N] [--after DATE] [--before DATE] [--output PATH]`
- `wacli messages show --chat JID --id MSG_ID`
- `wacli messages context --chat JID --id MSG_ID [--before N] [--after N]`
- `wacli send text --to RECIPIENT --message TEXT [--pick N] [--no-preview] [--reply-to MSG_ID] [--reply-to-sender JID] [--post-send-wait 2s]`
- `wacli send file --to RECIPIENT --file PATH [--pick N] [--caption TEXT] [--filename NAME] [--mime TYPE] [--ptt] [--reply-to MSG_ID] [--reply-to-sender JID] [--post-send-wait 2s]`
- `wacli send voice --to RECIPIENT --file PATH [--pick N] [--mime TYPE] [--reply-to MSG_ID] [--reply-to-sender JID] [--post-send-wait 2s]`
- `wacli send react --to PHONE_OR_JID --id MSG_ID [--reaction TEXT] [--sender JID] [--post-send-wait 2s]`
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
- `wacli profile set-picture IMAGE`
- `wacli doctor [--connect]`
- `wacli version`
- `wacli completion bash|zsh|fish|powershell [--no-descriptions]`
- `wacli help [command]`

`RECIPIENT` for `send text/file` accepts a JID, phone number, or synced contact/group/chat name. If a name is ambiguous, interactive terminals prompt; scripts can pass `--pick N`.

## Storage

Defaults to `~/.local/state/wacli` on Linux and `~/.wacli` elsewhere. Existing Linux `~/.wacli` stores are reused when the XDG state store does not exist. Override with `--store DIR`.

Global flags:

- `--store DIR`: store directory.
- `--json`: JSON output.
- `--events`: emit machine-readable NDJSON lifecycle events on stderr for long-running commands, including interrupt signals and command errors.
- `--full`: disable table truncation.
- `--timeout DURATION`: timeout for non-sync commands.
- `--lock-wait DURATION`: wait for the store lock before failing write commands.
- `--read-only`: reject commands that intentionally write WhatsApp or the local store.

## Environment overrides

- `WACLI_DEVICE_LABEL`: override the linked device label shown in WhatsApp (defaults to `wacli - <OS> (<hostname>)` when detectable).
- `WACLI_DEVICE_PLATFORM`: override the linked device platform (defaults to `DESKTOP`; invalid values fall back to `CHROME`).
- `WACLI_READONLY`: set to `1`, `true`, `yes`, or `on` to enable read-only mode.
- `WACLI_SYNC_MAX_MESSAGES`: stop `auth` bootstrap sync or `sync` before storing more than this many total local messages.
- `WACLI_SYNC_MAX_DB_SIZE`: stop `auth` bootstrap sync or `sync` when `wacli.db` plus SQLite sidecars reaches a size such as `500MB` or `2GB`.
- `WACLI_STORE_DIR`: override the default store directory.

## Backfilling older history

`wacli sync` stores whatever WhatsApp Web sends opportunistically. To try to fetch *older* messages, use on-demand history sync requests to your **primary device** (your phone).

Important notes:

- This is **best-effort**: WhatsApp may not return full history.
- Your **primary device must be online**.
- Requests are **per chat** (DM or group). `wacli` uses the *oldest locally stored message* in that chat as the anchor.
- Backfill skips automatic initial history-sync blob downloads and only processes on-demand responses, which keeps memory use bounded on small Linux/ARM devices.
- Recommended `--count` is `50` per request; maximum is `500`.
- Maximum `--requests` per run is `100`.

### Backfill one chat

```bash
pnpm wacli history backfill --chat 1234567890@s.whatsapp.net --requests 10 --count 50
```

### Backfill all chats (script)

This loops through chats already known in your local DB:

```bash
pnpm -s wacli -- --json chats list --limit 100000 \
  | jq -r '.data[].JID' \
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

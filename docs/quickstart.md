---
title: Quickstart
description: "Pair as a linked WhatsApp Web device, sync, search, and send your first message in under five minutes."
---

# Quickstart

Five minutes from a clean machine to authenticated sync, search, and send. For deeper reading, follow the links at the bottom of each step.

## 1. Install

```bash
brew install steipete/tap/wacli
wacli --version
```

Other options (release archives, source builds, GCC 15 notes) are documented on [Install](install.md).

## 2. Pair as a linked device

```bash
wacli auth
```

`auth` prints a QR code in your terminal. On your phone, open WhatsApp → **Linked devices** → **Link a device**, scan the QR, and approve. As soon as pairing succeeds, `auth` immediately starts the initial sync — keep it running until it idles out or press `Ctrl+C` once it has caught up.

If the terminal QR does not scan, try `--qr-format text` and render that raw QR payload in another app, or pair via phone-number code with `--phone +15551234567`.

> Refresh tokens last as long as the linked device stays linked on your phone. Unlinking from the phone (or `wacli auth logout`) ends the session and requires a fresh QR.

Verify:

```bash
wacli auth status
```

## 3. Keep the store warm

```bash
wacli sync --follow
```

`sync` never shows a QR; it requires a previously paired session and runs until you stop it. `--once` exits after one idle window; `--follow` reconnects on errors. Both honor `--max-messages` / `--max-db-size` (and the `WACLI_SYNC_MAX_*` env equivalents) so the local store stays bounded.

See [Sync](sync.md) for refresh-contacts/refresh-groups, `--download-media`, and the idle-exit knobs.

## 4. Search and read

```bash
# Full-text search (FTS5 when the binary was built with -tags sqlite_fts5; LIKE otherwise)
wacli messages search "meeting"

# Search media-bearing messages
wacli messages search "meeting" --has-media

# List recent messages from a chat, oldest first
wacli messages list --chat 1234567890@s.whatsapp.net --asc

# Show a single message
wacli messages show --chat 1234567890@s.whatsapp.net --id <message-id>

# Show context around a message
wacli messages context --chat 1234567890@s.whatsapp.net --id <message-id> --before 5 --after 5
```

`--json` produces a stable envelope; `--full` keeps full IDs in tables. See [Messages](messages.md) for every filter.

## 5. Send a message

```bash
# Send a text message by phone number, JID, or synced contact/group/chat name
wacli send text --to mom --message "hello"

# Send a quoted reply
wacli send text --to 1234567890 --message "replying" --reply-to <message-id>

# Send a file with a caption
wacli send file --to 1234567890 --file ./pic.jpg --caption "hi"

# Send a 512x512 WebP sticker
wacli send sticker --to 1234567890 --file ./sticker-512.webp

# Send a native voice note (OGG/Opus)
wacli send voice --to 1234567890 --file ./voice.ogg

# React (omit --reaction for the default thumbs-up; use --reaction "" to clear)
wacli send react --to 1234567890 --id <message-id> --reaction "🎉"

# Post a WhatsApp status broadcast
wacli send status --message "available today" --background-color '#1f7a8c'
```

Recipient resolution and disambiguation (`--pick N`, ambiguous-name prompts), link-preview behavior, status broadcasts, and post-send waits are documented in [Send](send.md).

## 6. Backfill older history (optional, best-effort)

`sync` only stores what WhatsApp Web pushes. To request older messages for a specific chat from your **primary device** (your phone), use:

```bash
wacli history coverage --include-blocked
wacli history fill --dry-run --limit 20
wacli history backfill --chat 1234567890@s.whatsapp.net --requests 10 --count 50
```

The phone must be online for `backfill`. WhatsApp may not return full history. See [History](history.md) for coverage planning, limits, and patterns.

## 7. Named accounts (optional)

If you run more than one WhatsApp number, named accounts give each an isolated store, session, and lock:

```bash
# Add a second account and pair it immediately
wacli accounts add work

# List all configured accounts
wacli accounts list

# Use a named account with any command
wacli --account work sync --follow
wacli --account personal send text --to mom --message "hi"
```

Use `--no-auth` to create the account entry without pairing immediately. Two accounts can sync concurrently — their locks are independent. See [Accounts](accounts.md) for YAML config and migration from manual `--store` paths.

## 8. Diagnostics and safety

```bash
wacli doctor
wacli doctor --connect

# Read-only mode for agents / sandboxes
wacli --read-only messages search "invoice"
WACLI_READONLY=1 wacli send text --to mom --message "hi"   # exits with a clear error
```

`doctor` checks the store, schema, FTS5 availability, and (with `--connect`) live connectivity. See [Doctor](doctor.md).

## 9. Shell completion (optional)

```bash
wacli completion bash    >> ~/.bash_completion
wacli completion zsh     >  "${fpath[1]}/_wacli"
wacli completion fish    >  ~/.config/fish/completions/wacli.fish
```

## Where next

- [Overview](overview.md) — global flags, store model, full command map.
- [Accounts](accounts.md) — named accounts, isolated stores, YAML config.
- [Send](send.md) — every recipient form, replies, reactions, mentions, link previews.
- [Channels](channels.md) — read and follow WhatsApp Channels.
- [Groups](groups.md) — list, refresh, info, rename, participants, invite links.
- [Spec](spec.md) — design notes, storage layout, locking model, non-goals.
- [Doctor](doctor.md) — self-checks and connectivity probe.

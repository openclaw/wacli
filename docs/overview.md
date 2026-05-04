# wacli overview

Read when: you need the user-facing command map, global flags, store model, or links to command-specific docs.

`wacli` is a WhatsApp CLI built on `whatsmeow`. It pairs as a linked WhatsApp Web device, stores message metadata locally, supports offline search, and exposes send/media/group/contact workflows for scripts and humans.

## Store and output

- Default store: `~/.local/state/wacli` on Linux, `~/.wacli` elsewhere.
- Existing Linux `~/.wacli` stores are reused when no XDG store exists.
- Override the store with `--store DIR` or `WACLI_STORE_DIR`.
- Human-readable tables are the default.
- Use `--json` for scriptable output.
- Use `--full` to avoid table truncation.
- Write commands acquire the store lock; use `--lock-wait DURATION` to wait.
- Use `--read-only` or `WACLI_READONLY=1` to reject commands that write WhatsApp or local state.

## Command pages

- [auth](auth.md) - pair, inspect auth status, logout.
- [sync](sync.md) - sync messages, contacts, groups, and optional media.
- [messages](messages.md) - list, search, show, and contextualize stored messages.
- [send](send.md) - send text, files, replies, and reactions.
- [media](media.md) - download media attached to stored messages.
- [contacts](contacts.md) - search contacts and manage local aliases/tags.
- [chats](chats.md) - list and show known chats.
- [groups](groups.md) - refresh, inspect, rename, leave, join, invite, and manage participants.
- [history](history.md) - request older per-chat history from the primary device.
- [presence](presence.md) - send typing/paused indicators.
- [profile](profile.md) - set the authenticated account profile picture.
- [doctor](doctor.md) - diagnose store, auth, search, and optional live connectivity.
- [version](version.md) - print the CLI version.
- [completion](completion.md) - generate shell completion scripts.
- [help](help.md) - inspect command help from the CLI.

## Common flow

```bash
wacli auth
wacli sync --follow
wacli messages search "meeting"
wacli send text --to mom --message "hello"
```

## Recipient formats

Commands that accept `PHONE_OR_JID` accept a WhatsApp JID like `1234567890@s.whatsapp.net`, a group JID like `123456789@g.us`, or a phone number with common formatting such as `+1 (234) 567-8900`.

`send text` and `send file` also accept synced contact, group, or chat names through `RECIPIENT`. If a name is ambiguous, interactive terminals prompt; scripts can use `--pick N`.

## History limits

WhatsApp Web history is best-effort. `wacli sync` stores events WhatsApp provides, and `wacli history backfill` can ask the primary phone for older messages per chat. It cannot guarantee a full account export.

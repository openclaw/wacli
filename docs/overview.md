# wacli overview

Read when: you need the user-facing command map, global flags, store model, or links to command-specific docs.

`wacli` is a WhatsApp CLI built on `whatsmeow`. It pairs as a linked WhatsApp Web device, stores message metadata locally, supports offline search, and exposes send/media/group/contact workflows for scripts and humans. Named accounts let multiple WhatsApp identities use isolated stores via `--account`.

## Store and output

- Default store: `~/.local/state/wacli` on Linux, `~/.wacli` elsewhere.
- Existing Linux `~/.wacli` stores are reused when no XDG store exists.
- Override the store with `--store DIR` or `WACLI_STORE_DIR`.
- Human-readable tables are the default.
- Use `--json` for scriptable output.
- Use `--full` to avoid table truncation.
- Write commands acquire the store lock; use `--lock-wait DURATION` to wait.
- Use `--read-only` or `WACLI_READONLY=1` to reject commands that write WhatsApp or local state.
- Use `sync --max-messages`, `sync --max-db-size`, `WACLI_SYNC_MAX_MESSAGES`, or `WACLI_SYNC_MAX_DB_SIZE` to bound local history growth.
- Use `store cleanup`, `chats cleanup`, and `groups prune` to preview and remove stale local rows after sync has already stored them.
- Authenticated startup resolves historical `@lid` chat/message rows to phone-number JIDs when the WhatsApp session store has the mapping.
- Companion tools should prefer `--json`, `--events`, webhooks, or read-only access to `wacli.db`; see [companion integrations](integrations.md).

## Command pages

- [auth](auth.md) - pair, inspect auth status, logout.
- [accounts](accounts.md) - create and select named account stores.
- [sync](sync.md) - sync messages, contacts, groups, channels, and optional media.
- [messages](messages.md) - list, search, show, and contextualize stored messages.
- [calls](calls.md) - list stored WhatsApp call events.
- [send](send.md) - send text, files, stickers, replies, and reactions.
- [media](media.md) - download media attached to stored messages.
- [contacts](contacts.md) - search contacts and manage local aliases/tags.
- [contacts import-system](contacts-import-system.md) - import macOS Contacts names into local contact metadata.
- [chats](chats.md) - list, show, filter, and manage known chat state.
- [groups](groups.md) - refresh, inspect, rename, leave, join, invite, and manage participants.
- [store](store.md) - inspect local store stats and prune stale local rows.
- [channels](channels.md) - list, inspect, join, leave, and send to WhatsApp Channels.
- [history](history.md) - inspect archive coverage and request older per-chat history from the primary device.
- [presence](presence.md) - send typing/paused indicators.
- [profile](profile.md) - set the authenticated account profile picture.
- [doctor](doctor.md) - diagnose store, auth, search, and optional live connectivity.
- [docs](docs.md) - print the hosted documentation URL.
- [version](version.md) - print the CLI version.
- [completion](completion.md) - generate shell completion scripts.
- [help](help.md) - inspect command help from the CLI.
- [companion integrations](integrations.md) - build read-only local tools on top of synced data.

## Common flow

```bash
wacli auth
wacli sync --follow
wacli messages search "meeting"
wacli send text --to mom --message "hello"
```

## Recipient formats

Commands that accept `PHONE_OR_JID` accept a WhatsApp JID like `1234567890@s.whatsapp.net`, a group JID like `123456789@g.us`, a channel JID like `123456789012345@newsletter`, or a phone number with common formatting such as `+1 (234) 567-8900`.

`send text`, `send file`, `send sticker`, and `send voice` also accept synced contact, group, or chat names through `RECIPIENT`. If a name is ambiguous, interactive terminals prompt; scripts can use `--pick N`.

`chats archive`, `chats pin`, `chats mute`, and `chats mark-read` use the same synced contact/group/chat resolver through `--chat`. Pass a raw JID when you need an exact target.

## History limits

WhatsApp Web history is best-effort. `wacli sync` stores events WhatsApp provides, and `wacli history backfill` can ask the primary phone for older messages per chat. It cannot guarantee a full account export.

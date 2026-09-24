# chats

Read when: listing known chats, filtering chat state, archiving/pinning/muting/marking chats, or pruning stale local chat rows.

`wacli chats` reads chat rows from `wacli.db`. It can use session-backed PN/LID mappings to make historical `@lid` chat rows display as phone-number chats when possible. State commands normally send WhatsApp app-state patches through the authenticated session and update the local index after WhatsApp accepts the change. Explicit receipt mode uses the independent network receipt path described below.

## Commands

```bash
wacli chats list [--query TEXT] [--limit N] [--archived|--no-archived] [--pinned|--no-pinned] [--muted|--no-muted] [--unread|--no-unread]
wacli chats show --jid JID
wacli chats archive --chat CHAT [--pick N]
wacli chats unarchive --chat CHAT [--pick N]
wacli chats pin --chat CHAT [--pick N]
wacli chats unpin --chat CHAT [--pick N]
wacli chats mute --chat CHAT [--duration DURATION] [--pick N]
wacli chats unmute --chat CHAT [--pick N]
wacli chats mark-read --chat CHAT [--pick N] [--receipts]
wacli chats mark-unread --chat CHAT [--pick N]
wacli chats cleanup [--days N] [--jid JID] [--dry-run] [--confirm]
```

## Notes

- `list` is local and sorted by pinned chats first, then newest known message timestamp.
- WhatsApp system events, such as a changed security code or a group notice, arrive as payloads with no content and are stored as `(message)` rows. They do not set that timestamp, so they cannot move a chat up the list; a chat that holds nothing else has no timestamp and sorts last.
- On the next writable open, activity matching the newest locally stored message is recomputed from stored content. Activity newer than every local message is preserved. If an unstored message advertised by history has the exact same second as a local placeholder, the repair uses local content; syncing that message restores its activity.
- `--query` filters by chat name or JID.
- `list --json` and `show --json` include `archived`, `pinned`, `muted_until`, `unread`, and `unread_count`; `unread` is true for counted unread messages and marker-only unread chats, while `unread_count` only counts unread messages.
- `list --unread` matches counted and marker-only unread chats; `list --no-unread` excludes both.
- Replayed read signals reduce the unread count only through the messages they cover; older reads cannot restore already-read messages. Known message IDs distinguish arrivals in the same second, using their local insertion order. Without a known ID boundary, messages at the cutoff second remain unread. Content-free system events, reactions, and revocations do not add to live unread counts.
- `mark-unread` sets the unread marker without inventing an unread count; `mark-read` clears the marker and count through its captured message boundary; arrivals beyond it remain unread.
- `show` accepts the stored JID. If a phone JID maps to a historical `@lid` row, it can show that row too.
- State commands use `--chat` and resolve names, phone numbers, groups, and JIDs like send commands. Use `--pick N` for ambiguous matches.
- After a same-store `sync --follow` process finishes startup and opens its local delegate socket, `mark-read` and `mark-unread` are delegated to it while it owns the store lock. Other state commands still require direct access to the lock.
- Restart an older `sync --follow` process after upgrading before using delegated read-state commands; older daemons return an unsupported `mark_read` kind error.
- State commands print a compact success line by default and a stable JSON object with `--json`.
- `mute --duration 0` or omitting `--duration` mutes forever. Use `unmute` to clear it.
- Run `wacli sync` to catch up chat-state changes made on other devices; run `wacli contacts refresh` to improve chat names.
- `cleanup` only deletes local `wacli.db` rows. It does not delete chats or messages from WhatsApp.
- `cleanup --days N` skips chats with no known local activity timestamp; use `--jid` for an explicit local row.
- Use `cleanup --dry-run` before deleting and `--confirm` only for scripts that already reviewed the target list.

## Read receipts

Use `wacli chats mark-read --chat CHAT --receipts` to send receipts for stored unread incoming messages. Plain `mark-read` keeps its existing app-state behavior and does not notify message senders. `mark-unread` remains available through a running sync process.

Receipt mode does not wait for app-state recovery, so it can run while the primary phone cannot provide an app-state snapshot. It works directly or through a same-store `sync --follow` process. Restart older sync processes after upgrading: the separate receipt request is rejected by an older daemon before it changes the chat.

Each call handles at most 100 messages, starting with the oldest messages in the current unread selection. Repeat the command for larger counts; unsent messages and arrivals during dispatch remain unread. Reactions, outgoing messages, content-free placeholders, and deleted messages are excluded before the limit. Group messages are batched by sender. Missing group sender identities or an unread count larger than the eligible local history fail before dispatch; sync the missing history first. A marker-only chat uses its latest eligible incoming message, or clears only the local marker when no such message is stored.

WhatsApp's read-receipt privacy setting is respected and never changed. JSON output adds `receipts`, the number of dispatched messages. When nonzero it also includes `receipt` and `sender_notified`:

- `receipt: "read-self"`, `sender_notified: false`: explicitly sent only to the account's own devices, including when receipts are disabled.
- `receipt: "unknown"`, `sender_notified: null`: normal receipts were requested, but the transport may apply a newer privacy setting and does not report the emitted type. This is not confirmation that a sender saw blue ticks. Mixed group-batch outcomes are also reported as unknown.

The local read boundary advances after every selected batch has dispatched successfully. If a later batch fails, the error reports how many messages were already dispatched; retrying may resend those receipts. Receipt mode does not send an additional chat-state patch. `--read-only` rejects it because sending receipts changes WhatsApp state.

## Examples

```bash
wacli chats list
wacli chats list --query family --limit 20
wacli chats list --pinned
wacli chats show --jid 1234567890@s.whatsapp.net
wacli chats mute --chat "+1 555 123 4567" --duration 8h
wacli chats mark-read --chat family --pick 1
wacli chats mark-read --chat family --pick 1 --receipts --json
wacli chats cleanup --days 365 --dry-run
```

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
wacli chats mark-read --chat CHAT [--pick N] [--through-message-id ID] [--receipts]
wacli chats mark-unread --chat CHAT [--pick N]
wacli chats cleanup [--days N] [--jid JID] [--dry-run] [--confirm]
```

## Notes

- `list` is local and sorted by pinned chats first, then newest known message timestamp.
- During sync, a new incoming content message unarchives its chat when WhatsApp's **Keep chats archived** setting is off. History imports, duplicate messages, edits, reactions, and outgoing messages do not trigger this. The incoming message must be newer than both the archive action and the account setting; within the same timestamp second, a first live delivery after a known action wins, including actions queued behind startup recovery. The message and boundary are stored atomically, and an older archive replay cannot undo the resulting unarchive.
- On upgrade, sync recovers the account setting and archive timestamps from `regular_low` before applying automatic unarchives. Unknown settings preserve the archived state. If full replay is known to fail verification, sync requests a phone recovery snapshot and keeps the recovery intent until it arrives; no incremental delta is treated as proof of the missing setting.
- WhatsApp system events, such as a changed security code or a group notice, arrive as payloads with no content and are stored as `(message)` rows. They do not set that timestamp, so they cannot move a chat up the list; a chat that holds nothing else has no timestamp and sorts last.
- On the next writable open, activity matching the newest locally stored message is recomputed from stored content. Activity newer than every local message is preserved. If an unstored message advertised by history has the exact same second as a local placeholder, the repair uses local content; syncing that message restores its activity.
- `--query` filters by chat name or JID.
- `list --json` and `show --json` include `archived`, `pinned`, `muted_until`, `unread`, and `unread_count`; `unread` is true for counted unread messages and marker-only unread chats, while `unread_count` only counts unread messages.
- `list --unread` matches counted and marker-only unread chats; `list --no-unread` excludes both.
- Replayed read signals reduce the unread count only through the messages they cover; older reads cannot restore already-read messages. Known message IDs distinguish arrivals in the same second, using their local insertion order. Without a known ID boundary, messages at the cutoff second remain unread. Content-free system events, reactions, and revocations do not add to live unread counts.
- `mark-unread` sets the unread marker without inventing an unread count; `mark-read` clears the marker and count through its captured message boundary; arrivals beyond it remain unread.
- `mark-read --through-message-id ID` fixes that boundary to an exact stored inbound message in the resolved chat, before app-state recovery starts. Missing, outgoing, deleted, revoked, reaction, and content-free placeholder anchors fail before any read update. Newer messages, including later rows in the same second, remain unread. Without this flag, mark-read keeps its existing behavior.
- Reading a chat on the phone clears it here too while `sync` is connected. WhatsApp reports that read as a `read-self` receipt only while read receipts are turned off; with them on it arrives as an ordinary read receipt sent by this account, and both are honoured.
- `show` accepts the stored JID. If a phone JID maps to a historical `@lid` row, it can show that row too.
- State commands use `--chat` and resolve names, phone numbers, groups, and JIDs like send commands. Use `--pick N` for ambiguous matches.
- After a same-store `sync --follow` process finishes startup and opens its local delegate socket, all state commands are delegated to it while it owns the store lock.
- `mark-read` and `mark-unread` run in the follow process's serialized delegate queue. `archive`, `unarchive`, `pin`, `unpin`, `mute`, and `unmute` run outside it, so an app-state sync or recovery before the change cannot hold queued sends; the caller's `--timeout` still bounds the operation.
- Restart an older `sync --follow` process after upgrading before using delegated state commands; older daemons return an unsupported `mark_read` or `chat_state` kind error.
- State commands print a compact success line by default and a stable JSON object with `--json`.
- `mute --duration 0` or omitting `--duration` mutes forever. Use `unmute` to clear it.
- Run `wacli sync` to catch up chat-state changes made on other devices; run `wacli contacts refresh` to improve chat names.
- `cleanup` only deletes local `wacli.db` rows. It does not delete chats or messages from WhatsApp.
- `cleanup --days N` skips chats with no known local activity timestamp; use `--jid` for an explicit local row.
- Use `cleanup --dry-run` before deleting and `--confirm` only for scripts that already reviewed the target list.

## WhatsApp Business quick replies

```bash
wacli --json quick-replies list
wacli quick-replies create --shortcut hello --message "Hello" --keyword greeting
wacli quick-replies edit ID_OR_SHORTCUT --message "Updated" [--keyword K]...
wacli quick-replies edit ID_OR_SHORTCUT --clear-keywords
wacli quick-replies delete ID_OR_SHORTCUT
wacli --json app-state resync regular
```

These commands are independent of WhatsApp Lists and label management. Quick
replies are text-only app-state values; images on phone replies are not mirrored.
Writes use `regular`, with the existing pre-write sync, shared persistence owner,
apply boundary and durable recovery-debt handling. Omitted edit fields retain the
stored shortcut, message, keywords, count and associated label IDs. Matching
accepts shortcuts with or without `/`; ambiguity and database errors fail closed.
Unix-seconds IDs follow Baileys and skip known IDs, including tombstones; unseen
same-second IDs may still collide. Only Business accounts display quick replies.

The action version is **3**, observed on two of three accounts; the third sent 2.
whatsmeow's `events.AppState` omits action-version metadata, so account-specific
version following is unavailable. The dependency is unchanged.

The regular send boundary uses the pinned SDK's encode/apply/fetch primitives,
returning send-result and single-409-retry batches synchronously to the owner.
It does not use the SDK's detached send-echo dispatch. The owner persists the
whole batch before releasing the writer, so an earlier echo cannot overwrite
a following edit or undo a deletion. Device timestamps remain data, not clocks
for arbitration. Existing replay-debt retention remains conservative: the next
write or follow-mode recovery normally replays `regular` to settle that debt.

When `sync --follow` owns the store, writes use the `app_state_write` delegate;
resync uses the separate `app_state_resync` kind. Neither holds the paced send
queue. Old owners reject unsupported kinds; upgrade and restart `wacli sync`.
Read-only mode refuses writes and resync but permits offline listing.

Listing returns `data = {quick_replies: [...], completeness: "complete"|"incomplete"}`.
Independent reply coverage starts incomplete on upgrade, including stores with
historical Lists full-sync metadata. Missing tables on read-only opens also mean
incomplete; text output warns. Only a successfully persisted reply-aware full
`regular` replay marks coverage complete. Snapshots merge rather than prune;
generic REMOVE mutations are not dispatched by the pinned SDK, so completeness
is not proof that every phone-side deletion or feature was captured.

`app-state resync regular` reuses existing recovery and returns
`data = {collection: "regular", completeness, quick_replies: N}`. There is no
Lists count or force override. Known `app_state_full_sync_broken` collections
always refuse and point to `sync --follow` primary recovery: stock whatsmeow
discards saved collection state before a full sync verifies. Keep the primary
online for recovery; resync does not bypass verification or guarantee repair.

The next writable open adds migration **38**; 34-37 are claimed by open PRs
(stickers, ad referrals, lists, message kinds). The schema guard also handles occupied migration rows from other
builds/open PRs without overwriting their data or version entries. This feature
does not depend on the Lists prerequisite or implement any label commands.

## Read receipts

Read events carrying message IDs are applied as bounded ranges, including live echoes and later recovery replay. If newer readable messages remain, an existing marker-only unread state is preserved even when the event came from another device or a plain mark-read. Events containing only a timestamp keep their existing clearing behavior. Replaying an older range never restores a marker that has already been cleared.

Use `wacli chats mark-read --chat CHAT --receipts` to send receipts for stored unread incoming messages. Plain `mark-read` keeps its existing app-state behavior and does not notify message senders. `mark-unread` remains available through a running sync process.

Add `--through-message-id ID` to send receipts only for the current unread selection at or before that inbound message. The 100-message cap, group sender batching, and read-receipt privacy setting still apply. Repeat with the same ID to finish a capped selection; newer messages remain unread. Both direct execution and delegation honor this cutoff. Older sync processes reject the separate bounded request without falling back to an unbounded read; restart `wacli sync` after upgrading.

Receipt mode does not wait for app-state recovery, so it can run while the primary phone cannot provide an app-state snapshot. It works directly or through a same-store `sync --follow` process. Restart older sync processes after upgrading: the separate receipt request is rejected by an older daemon before it changes the chat.

Each call handles at most 100 messages, starting with the oldest messages in the current unread selection. Repeat the command for larger counts; unsent messages and arrivals during dispatch remain unread. Reactions, outgoing messages, content-free placeholders, and deleted messages are excluded before the limit. Group messages are batched by sender. Missing group sender identities or an unread count larger than a nonempty eligible local history fail before dispatch; sync the missing history first. A marker-only chat uses its latest eligible incoming message.

If no eligible incoming message is stored, receipt mode clears only the local unread count and marker, even when the phone reported an unread count. It sends no receipt or app-state patch and does not acknowledge unseen messages or change the phone's unread state. A newly stored eligible message prevents the local clear. Zero-receipt results include `local_only: true` in JSON; text output says `local state only; no messages acknowledged`. Sync the missing history if you need receipts for those messages.

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
wacli chats mark-read --chat family --pick 1 --through-message-id HANDLED_MESSAGE_ID
wacli chats cleanup --days 365 --dry-run
```

When the primary phone answers an app-state recovery after the command times out, a running process continues waiting for up to 24 hours and persists that snapshot. Closing the process stops that wait; durable recovery intents remain available for a later retry. A collection whose full snapshot fails hash verification retains a durable broken-snapshot flag. Subsequent writes can use an incremental sync only when the SDK has local collection state; older replay debt remains until a verified full sync or primary recovery covers it. This does not guarantee that the local chat index already includes every server-side mutation.

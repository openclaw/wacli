# messages

Read when: listing, searching, exporting, showing, inspecting local message context, or mutating stored messages.

Most `wacli messages` commands read from the local store. `messages edit`, `messages delete`, `messages revoke`, and `messages forward` are remote WhatsApp mutations and require an authenticated, writable store.
WhatsApp status broadcasts are stored separately in `status_messages`; they are not returned by `messages list`, `messages search`, or `messages export`.

## Commands

```bash
wacli messages list [--chat JID] [--sender JID] [--from-me|--from-them] [--asc] [--limit N] [--after DATE] [--before DATE] [--forwarded] [--starred]
wacli messages search <query> [--chat JID] [--from JID] [--has-media] [--type text|image|video|audio|document] [--forwarded] [--starred] [--limit N] [--after DATE] [--before DATE]
wacli messages starred [--chat JID] [--limit N] [--after DATE] [--before DATE] [--asc]
wacli messages export [--chat JID] [--limit N] [--after DATE] [--before DATE] [--output PATH]
wacli messages show --chat JID --id MSG_ID
wacli messages context --chat JID --id MSG_ID [--before N] [--after N]
wacli messages edit --chat JID --id MSG_ID --message TEXT [--post-send-wait 2s]
wacli messages delete --chat JID --id MSG_ID [--for-me] [--delete-media] [--post-send-wait 2s]
wacli messages purge --chat JID --id MSG_ID [--dry-run] [--confirm]
wacli messages revoke --chat JID --id MSG_ID [--post-send-wait 2s]
wacli messages forward --chat JID --id MSG_ID --to RECIPIENT [--pick N] [--post-send-wait 2s]
```

## Pagination and freshness

`list`, `search`, and `export` accept `--cursor TOKEN`. Repeat the original
query and filters; `--limit` can change. List defaults to newest first, export
to oldest first, and FTS search to relevance. Limits of zero or less retain the
legacy default of 50.

JSON keeps `data.messages` and `data.fts`, adding
`page: {has_more, next_cursor}` and
`source: {last_sync_at, scope: "local_archive"}`. An extra matching row determines
`has_more`. A cursor is returned only when there is another page and the store
supports cursors. Human output prints a continuation hint on stderr.

Cursors bind to the store, command, filters, order, and search backend. Message
writes, name/star changes, pruning, identity normalization, and index rebuilds
invalidate them. Restart at the first page after an invalidation; do not advance
`--after` by one second, because messages can share timestamps. Reads use one
SQLite view for rows, revision, and freshness. No snapshot files are created.

Old stores opened read-only keep first-page reads but cannot emit or consume
cursors until the next ordinary writable store open applies migrations.
`last_sync_at` is null until a successful connected one-shot/bootstrap sync or
an offline-backlog completion is recorded. Doctor uses this same state.
Heartbeats and the newest message timestamp do not establish successful sync.

All results describe retained local history only. A finished page traversal and
a recent sync do not certify complete WhatsApp history or continuous connectivity.

## Incremental changes

```bash
wacli --read-only --json messages changes --checkpoint
wacli --read-only --json messages changes --since beginning --limit 100
wacli --read-only --json messages changes --since CHECKPOINT --limit 100
```

This command never connects to WhatsApp. Supply `--since` or `--checkpoint`;
there are no chat/query filters. `data` contains `changes`, `has_more`, and
`next_checkpoint`, plus `store_id` for deduplication. A checkpoint captures the current sequence without reading
historical entries. It is distinct from a pagination cursor.

Records contain a store-local sequence, local recording time, message time,
operation, chat JID/message ID, and a reason when known. Operations are `upsert`,
`delete`, and `identity_move`. Identity moves include the new chat identity;
remove the old key and fetch the new key. Local cleanup emits `local_removal`;
remote tombstones retain their existing WhatsApp reason; payload purge emits
`hard_purge`. Message time never controls sequence ordering.

The journal has no text, captions, media, filenames, or credentials. Fetch current
state with existing reads. Intermediate content versions cannot be reconstructed.
Stored message columns are meaningful, excluding the internal edit-order timestamp
and changes to an already-recorded deletion time. Identical stored values produce
no journal entry. Sync/history/backfill, local sends, edits, deletion, cleanup, and
identity normalization share atomic SQLite mutation triggers. Rollback exposes
neither the message mutation nor its change record.

A paginated drain captures a high-water boundary. New arrivals wait until the
next drain. Save `next_checkpoint` only after processing a successful response.
Retry the same checkpoint after failure; deduplicate by store identity and sequence.
This gives replay-safe reads, not exactly-once downstream processing.

Migration atomically seeds payload-free `upsert` references for every retained
message, including tombstones, without inventing past edits or removals. Read-only
unmigrated stores reject changes with writable-migration guidance.

Writable opens also reconcile pre-release retrieval migration numbers with the
receipt schema, preserving existing store identities and journal checkpoints.
Read-only opens never repair migration state.

Retention is **30 days, capped at 100,000 entries**, whichever removes older
entries first. Cleanup runs on writable opens and journal insertions. Reads never
perform cleanup. `beginning` and interrupted drains reject explicitly when their
unread range expires. If the seeded baseline exceeds the cap, `beginning` expires
immediately; retained messages and search results remain unchanged. Hard purge
removes retained journal references for that message and expires checkpoints whose
replay range now has a gap. The sequence high-water survives retention deletes.

To rebuild after expiry: capture a fresh checkpoint **before** a full paginated
export, process the export, then replay changes since that checkpoint. If a write
invalidates the export, discard its partial pages and restart it with the original
checkpoint. Keeping that anchor also catches arrivals with old message timestamps.
If the checkpoint expires during recovery, start a fresh recovery and report the
unrecoverable replay gap. All of this covers the local archive only.

## Search

Add `--context-before N --context-after N` to include visible chronological
neighbors around each hit. Each count is 0..20; context requests allow at most
100 hits per page. Zero/zero preserves the original output. JSON leaves matches
in `data.messages` and adds `data.context`, an array keyed by `chat_jid` and
`msg_id`, with a `messages` collection that includes the hit. Human output labels
context separately and marks the hit. Pagination counts hits only; context can
fall outside query and time filters. Deleted neighbors remain hidden. Verified
phone/LID equivalents share context, ordered by timestamp and row ID.

`--chat`, search `--from`, and list `--sender` accept numbers, JIDs, or exact
case-insensitive local names. Chat filters also match chat names. Sender filters
match local contact names and aliases. Explicit numbers/JIDs take precedence.
No network lookup or address-book change occurs. Missing names return an error;
ambiguous names list up to ten candidates and require an explicit JID.

```bash
wacli --read-only --json messages search invoice --chat "Ada" --context-before 3 --context-after 2
wacli --read-only --json messages list --sender "Ada" --limit 100
```

- Uses SQLite FTS5 when the binary was built with `-tags sqlite_fts5`.
- Falls back to `LIKE` if FTS5 is not available.
- `--type` accepts `text`, `image`, `video`, `audio`, or `document`.
- Shared WhatsApp contact cards are stored as searchable text with contact names and phone numbers when WhatsApp includes a vCard payload, including iPhone cards that group properties as `item1.TEL` and `item1.FN`.
- Associated-child and group-status-mention wrappers retain their inner text, media, and reply context. Group invitations expose their caption, with the group name as a fallback. These parser fixes apply on re-ingestion; they cannot recover absent payloads or missing decryption keys.
- History sync unwraps disappearing-message, view-once, and document-with-caption envelopes the same way live messages are unwrapped, so their text, media, and reply context are stored instead of `(message)`.
- Comment payloads retain their inner text or media and their envelope's reply target. Album headers show expected image/video counts; those summaries do not recover missing child captions or undecryptable history.
- `--starred` restricts list/search results to messages marked as starred by WhatsApp.
- Time filters accept RFC3339 or `YYYY-MM-DD`.

## Delivery state

Messages you sent carry `DeliveredTo` and `ReadBy`: how many recipients reported the message delivered, and how many of those went on to read it. A direct chat counts one recipient; a group counts one per participant who reported, so a chat where everybody has read is distinguishable from one where a single member has. Playing a voice note counts as reading it, and a state never moves backwards.

`messages show` reports the same two counts, and an identity migration from a `@lid` chat to its phone number carries the reports with their messages, counting a recipient that reported under both identities once.

Only reports that arrive while `sync` is connected are kept: WhatsApp announces each one once and does not repeat it. Messages sent before a store started keeping them, or while nothing was connected, stay at zero on both counts, which means "sent, nothing reported" rather than "not delivered". Your own notes chat receives no report at all, since the only recipient is the account itself. Reports about incoming messages are not kept: they say nothing about your own.

## Media captions

Plain audio messages have an empty `MediaCaption`. Their `Text` keeps the `[Audio]` display fallback, so they can still match searches for `Audio`. Text supplied alongside an audio payload remains its caption, including a literal `[Audio]` supplied by the sender. Existing rows are not migrated; an ordinary live or history re-ingestion can replace a legacy synthetic caption, subject to the existing edit and deletion rules.

## Starred

- `messages starred` lists starred messages ordered by star time when app-state events provide it; history-imported rows fall back to message time.
- `--after` and `--before` on `messages starred` filter by that stored star time.
- Starred state is imported from history sync and app-state star/unstar events.

## Export

- `messages export` writes a JSON export envelope with messages ordered oldest first.
- Use `--chat` to export one chat, or omit it to export recent messages across chats.
- Use `--after` and `--before` to bound the exported time window.
- Use `--output` to write the JSON export to a file.

## Edit and Delete

- `messages edit` updates one of your own recent sent text messages. WhatsApp only accepts edits inside its current edit window.
- `messages delete` revokes one of your own sent messages for everyone. `messages revoke` is the explicit form of the same delete-for-everyone operation.
- `messages delete --for-me` removes a stored message only for your WhatsApp account using WhatsApp's `deleteMessageForMe` app-state patch; it can target messages sent by you or by others. `--delete-media` is only valid with `--for-me`.
- `messages forward` forwards a stored text, image, video, GIF, audio, sticker, or document message to another recipient and marks the outgoing copy as forwarded. Media forwards require synced media metadata; reaction forwarding is not supported.
- These commands look up the target in the local store first and honor `--read-only`/`WACLI_READONLY`. Delete-for-everyone and edit require a message sent by you.
- Deleted messages and WhatsApp delete-for-me events are kept as local tombstones with `deleted_at` and `deletion_reason`. Their original text, reply, interactive, and media metadata remains available to direct `messages show`, but tombstones stay hidden from normal list/search/starred/export results and FTS.
- Sync, history, and backfill ingestion merge messages by chat JID and message ID. A message missing from any partial import is left unchanged, and a later live copy does not resurrect an existing tombstone.
- `messages purge` is the deliberate payload-erasure path. It only accepts an already tombstoned row, removes downloaded local media, clears its retained `wacli.db` payload, and requires confirmation unless `--confirm` is passed. A minimal tombstone with `payload_purged_at` and a non-cascading purge-ledger key remain so later sync or history imports cannot restore the payload after chat cleanup.

## LID mapping

When a phone-number chat JID maps to a stored `@lid` row, list/search/show/context include the mapped rows so historical LID splits do not hide messages.

## Examples

```bash
wacli messages list --chat 1234567890@s.whatsapp.net --asc
wacli messages list --from-me --limit 20
wacli messages starred --limit 20
wacli messages search "invoice" --has-media --type document
wacli messages search "invoice" --starred
wacli messages export --chat 1234567890@s.whatsapp.net --after 2024-01-01 --before 2024-02-01 --output messages.json
wacli messages show --chat 1234567890@s.whatsapp.net --id ABC123
wacli messages context --chat 1234567890@s.whatsapp.net --id ABC123 --before 3 --after 3
wacli messages edit --chat 1234567890@s.whatsapp.net --id ABC123 --message "updated text"
wacli messages delete --chat 1234567890@s.whatsapp.net --id ABC123
wacli messages delete --chat 1234567890@s.whatsapp.net --id ABC123 --for-me
wacli messages purge --chat 1234567890@s.whatsapp.net --id ABC123 --dry-run
wacli messages revoke --chat 1234567890@s.whatsapp.net --id ABC123
wacli messages forward --chat 1234567890@s.whatsapp.net --id ABC123 --to "Family"
```

Raw message text, captions, and explicit edits retain their original whitespace in stored records and JSON output. Human display text remains normalized. Content trimmed by an older version cannot be reconstructed from the local store alone.

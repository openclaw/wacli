# messages

Read when: listing, searching, exporting, showing, or inspecting local message context.

Most `wacli messages` commands read from the local store. `messages edit` and `messages delete` are remote WhatsApp mutations and require an authenticated, writable store.
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
```

## Search

- Uses SQLite FTS5 when the binary was built with `-tags sqlite_fts5`.
- Falls back to `LIKE` if FTS5 is not available.
- `--type` accepts `text`, `image`, `video`, `audio`, or `document`.
- Shared WhatsApp contact cards are stored as searchable text with contact names and phone numbers when WhatsApp includes a vCard payload.
- `--starred` restricts list/search results to messages marked as starred by WhatsApp.
- Time filters accept RFC3339 or `YYYY-MM-DD`.

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
- `messages delete` revokes one of your own sent messages for everyone.
- `messages delete --for-me` removes a stored message only for your WhatsApp account using WhatsApp's `deleteMessageForMe` app-state patch; it can target messages sent by you or by others. `--delete-media` is only valid with `--for-me`.
- Both commands look up the target in the local store first and honor `--read-only`/`WACLI_READONLY`. Delete-for-everyone and edit require a message sent by you.
- Deleted messages and WhatsApp delete-for-me events are kept as local tombstones for direct `messages show`, but are hidden from normal list/search/starred/export results.

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
```

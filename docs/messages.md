# messages

Read when: listing, searching, exporting, showing, or inspecting local message context.

`wacli messages` reads from the local store. It does not connect to WhatsApp unless a display path needs session-backed LID mapping.

## Commands

```bash
wacli messages list [--chat JID] [--sender JID] [--from-me|--from-them] [--asc] [--limit N] [--after DATE] [--before DATE] [--forwarded]
wacli messages search <query> [--chat JID] [--from JID] [--has-media] [--type text|image|video|audio|document] [--forwarded] [--limit N] [--after DATE] [--before DATE]
wacli messages export [--chat JID] [--limit N] [--after DATE] [--before DATE] [--output PATH]
wacli messages show --chat JID --id MSG_ID
wacli messages context --chat JID --id MSG_ID [--before N] [--after N]
```

## Search

- Uses SQLite FTS5 when the binary was built with `-tags sqlite_fts5`.
- Falls back to `LIKE` if FTS5 is not available.
- `--type` accepts `text`, `image`, `video`, `audio`, or `document`.
- Time filters accept RFC3339 or `YYYY-MM-DD`.

## Export

- `messages export` writes a JSON export envelope with messages ordered oldest first.
- Use `--chat` to export one chat, or omit it to export recent messages across chats.
- Use `--after` and `--before` to bound the exported time window.
- Use `--output` to write the JSON export to a file.

## LID mapping

When a phone-number chat JID maps to a stored `@lid` row, list/search/show/context include the mapped rows so historical LID splits do not hide messages.

## Examples

```bash
wacli messages list --chat 1234567890@s.whatsapp.net --asc
wacli messages list --from-me --limit 20
wacli messages search "invoice" --has-media --type document
wacli messages export --chat 1234567890@s.whatsapp.net --after 2024-01-01 --before 2024-02-01 --output messages.json
wacli messages show --chat 1234567890@s.whatsapp.net --id ABC123
wacli messages context --chat 1234567890@s.whatsapp.net --id ABC123 --before 3 --after 3
```

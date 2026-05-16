# store

Read when: inspecting local SQLite size/counts or pruning old local chat/group rows.

`wacli store` manages the selected account's local `wacli.db` mirror. Cleanup commands only delete local wacli cache/history rows; they do not delete WhatsApp chats, leave groups, or remove messages from WhatsApp servers.

## Commands

```bash
wacli store stats
wacli store cleanup [--days N] [--dry-run] [--confirm]
```

Related cleanup commands:

```bash
wacli chats cleanup [--days N] [--jid JID] [--dry-run] [--confirm]
wacli groups prune [--days N] [--left-only=false|--include-active] [--dry-run] [--confirm]
```

## Notes

- `store stats` reads local counts for chats, groups, left groups, and normal chat messages.
- Status broadcasts are persisted separately in `status_messages`; they are not chat rows and are not included in normal chat/message cleanup paths.
- `store cleanup` removes chats whose known local activity is older than `--days` and deletes their messages through the SQLite chat/message cascade.
- `chats cleanup --jid JID` removes one local chat row and its local messages.
- `groups prune` removes local group metadata plus the matching local chat/messages for pruned group JIDs.
- `groups prune` defaults to groups you have left. `--days N` limits that to groups left more than `N` days ago.
- `groups prune --include-active --days N` also prunes active groups whose last known local message is older than `N` days. Groups with no known local activity timestamp are skipped.
- Destructive cleanup commands require confirmation unless `--confirm` is passed.
- Use `--dry-run` first; it lists what would be deleted without changing the local store.
- Use `--read-only` or `WACLI_READONLY=1` to make cleanup commands fail before opening the store for writes.
- Use `--account NAME` to target a named account store. Use `--store DIR` for manual stores or migration debugging; it cannot be combined with `--account`.

## Examples

```bash
wacli store stats
wacli store cleanup --days 365 --dry-run
wacli chats cleanup --jid 1234567890@s.whatsapp.net --dry-run
wacli groups prune --dry-run
wacli groups prune --days 180 --dry-run
wacli groups prune --include-active --days 365 --dry-run
```

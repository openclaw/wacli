# companion integrations

Read when: building a local analytics, search, CRM, or agent-side companion tool on top of synced `wacli` data.

`wacli` is intentionally useful from scripts without becoming a plugin host. Companion tools should prefer stable CLI output first, then use read-only SQLite access when they need low-latency local queries or their own derived database.

## Integration surfaces

- Use `--json` for one-shot command output from `chats`, `contacts`, `groups`, `messages`, and `doctor`.
- Use `--events` for line-delimited lifecycle events from long-running `auth`, `sync`, and `history backfill` commands.
- Use `sync --webhook` for live-message delivery to another process or service.
- Use a read-only SQLite connection to `<store>/wacli.db` for local analytics that need joins, cursors, or incremental scans.

Prefer the CLI or webhook when possible. Direct SQLite reads are powerful, but the schema can evolve between releases.

## Store paths

The default store is:

- Linux: `~/.local/state/wacli`, with legacy `~/.wacli` reused when present.
- macOS and other platforms: `~/.wacli`.

Override with `--store DIR` or `WACLI_STORE_DIR`.

The store contains two SQLite databases:

- `session.db`: owned by `whatsmeow`; contains linked-device identity and keys.
- `wacli.db`: owned by `wacli`; contains chats, contacts, groups, messages, media metadata, and local state.

Companion tools should not read or write `session.db` unless they are explicitly working on WhatsApp session internals. Never write to `wacli.db` from a companion tool.

## Read-only SQLite

Open the database in SQLite read-only mode:

```bash
sqlite3 "file:$HOME/.wacli/wacli.db?mode=ro" \
  "SELECT chat_jid, msg_id, datetime(ts, 'unixepoch') AS at, display_text
   FROM messages
   WHERE revoked = 0 AND deleted_for_me = 0
   ORDER BY ts DESC
   LIMIT 20"
```

In Python:

```python
from pathlib import Path
import sqlite3

db = Path.home() / ".wacli" / "wacli.db"
conn = sqlite3.connect(f"file:{db}?mode=ro", uri=True)
conn.row_factory = sqlite3.Row

rows = conn.execute("""
    SELECT chat_jid, msg_id, sender_jid, sender_name, ts, display_text
    FROM messages
    WHERE revoked = 0 AND deleted_for_me = 0
    ORDER BY ts DESC
    LIMIT ?
""", (50,)).fetchall()
```

Avoid `immutable=1` when `wacli sync --follow` may be writing concurrently; a normal read-only SQLite connection can see WAL updates safely.

## Common queries

Recent human-visible messages:

```sql
SELECT
  m.chat_jid,
  COALESCE(m.chat_name, c.name, '') AS chat_name,
  m.msg_id,
  m.sender_jid,
  COALESCE(m.sender_name, '') AS sender_name,
  m.ts,
  COALESCE(m.display_text, m.text, '') AS text
FROM messages m
LEFT JOIN chats c ON c.jid = m.chat_jid
WHERE m.revoked = 0
  AND m.deleted_for_me = 0
ORDER BY m.ts DESC
LIMIT 100;
```

Incremental scan cursor:

```sql
SELECT rowid, chat_jid, msg_id, sender_jid, ts, display_text
FROM messages
WHERE rowid > ?
ORDER BY rowid ASC
LIMIT 1000;
```

Known chats by newest activity:

```sql
SELECT jid, kind, name, last_message_ts, archived, pinned, muted_until, unread
FROM chats
ORDER BY COALESCE(last_message_ts, 0) DESC
LIMIT 100;
```

Community subgroups:

```sql
SELECT jid, name, linked_parent_jid
FROM groups
WHERE linked_parent_jid IS NOT NULL
ORDER BY name;
```

## Privacy and safety

- Store derived data in your own database, not in `wacli.db`.
- Treat JIDs, display names, message text, media filenames, and local media paths as sensitive.
- Hash JIDs with a tool-local salt if you only need stable identity buckets.
- Provide a delete or opt-out path if the companion tool tracks people.
- Do not copy `session.db`, media keys, or WhatsApp device keys into unrelated systems.
- Use `WACLI_READONLY=1` when shelling out to `wacli` from a tool that should never mutate WhatsApp or the local store.

## Speaker-tracking pattern

A speaker tracker can stay small and non-invasive:

1. Run `wacli sync --follow` separately to keep the store warm.
2. Keep a cursor using the largest processed `messages.rowid`.
3. Read only new rows from `messages` in read-only mode.
4. Skip `from_me` rows if you only want contacts.
5. Hash `sender_jid` before writing to the tool database.
6. Store counts, first/last seen timestamps, and opt-out state in the tool database.

This pattern keeps `wacli` responsible for WhatsApp sync and keeps the companion tool responsible only for its derived local state.

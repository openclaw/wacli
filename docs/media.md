# media

Read when: downloading media from a synced message.

`wacli media` downloads media referenced by messages already stored in `wacli.db`.

## Commands

```bash
wacli media download --chat JID --id MSG_ID [--output PATH]
wacli media backfill [--chat JID] [--limit N] [--workers N]
```

## download

Downloads media for a single message.

### Notes

- The target message must already be synced.
- Media downloads are capped at 100 MiB.
- `--output` may be a file path or directory.
- If `--output` is omitted, media is written under the store media directory.
- `--read-only` is supported only with explicit `--output`; it writes the file without opening the WhatsApp session store or recording `local_path` / `downloaded_at`.

### Examples

```bash
wacli media download --chat 1234567890@s.whatsapp.net --id ABC123
wacli media download --chat 1234567890@s.whatsapp.net --id ABC123 --output ./downloads
wacli media download --chat 1234567890@s.whatsapp.net --id ABC123 --output ./photo.jpg
wacli --read-only media download --chat 1234567890@s.whatsapp.net --id ABC123 --output /tmp/photo.jpg
```

## backfill

Downloads media for every already-synced message that has downloadable metadata
but no local copy yet, over a single connection.

`sync --download-media` only downloads media for messages that *arrive during*
the sync session. Media for messages synced earlier is never fetched by sync;
`media backfill` closes that gap by scanning existing rows.

### Notes

- Files are written under the store media directory (same layout as `download`).
- `--chat` scopes the backfill to a single chat JID.
- `--limit` caps how many files to download (0 = all); newest messages first.
- `--workers` sets the number of concurrent downloads (default 4).
- Runs until completion or interruption by default; explicitly set global `--timeout` to cap a run.
- Requires a writable store; not available in `--read-only` mode.
- Reports counts: pending (total matching), attempted, downloaded, skipped, failed.

### Examples

```bash
wacli media backfill                                   # download all pending media
wacli media backfill --limit 50                        # download the 50 newest pending
wacli media backfill --chat 1234567890@s.whatsapp.net  # one chat only
wacli media backfill --json                            # machine-readable counts
```

# media

Read when: downloading media from a synced message.

`wacli media` downloads media referenced by messages already stored in `wacli.db`.

## Commands

```bash
wacli media download --chat JID --id MSG_ID [--output PATH]
wacli media backfill [--chat JID] [--limit N] [--workers N]
wacli media retry [--chat JID] [--before YYYY-MM-DD] [--limit N] [--batch N] [--wait DUR]
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

## retry

Recovers media that expired off WhatsApp's CDN. `media download` and `media
backfill` fetch directly from WhatsApp's servers, which only keep media for a
limited time — older media returns HTTP 403. `media retry` instead asks the
primary device (your phone) to re-upload the media via WhatsApp's media-retry
protocol (the same mechanism WhatsApp Web uses), then downloads it.

Recovery only works while the phone is online and still holds the media. Media
the phone no longer has is marked unavailable only after the stored CDN path is
also confirmed expired, so later runs can skip genuinely unavailable rows.

### Notes

- Requires a writable store and an online phone; not available in `--read-only` mode.
- Run `media backfill` first; retry is intended for media whose direct CDN download failed.
- Retry receipts are sent in batches (`--batch`, default 32) with a second
  attempt for non-responders; `--wait` (default 30s) bounds each attempt.
- `--chat` scopes to one chat; `--before YYYY-MM-DD` scopes to media older than a date.
- `--limit` caps how many messages to retry (0 = all pending); newest first.
- Runs until completion or interruption by default; explicitly set global `--timeout` to cap a run.
- Reports counts: requested, recovered, not_on_phone (gone), no_response, failed.
- `no_response` means the phone did not answer in time (often transient) — those
  stay pending and can be retried later; only `not_on_phone` is marked gone.

### Examples

```bash
wacli media retry                                    # try to recover all pending media
wacli media retry --chat 1234567890@s.whatsapp.net   # one chat only
wacli media retry --before 2026-01-01                # only media older than a date
wacli media retry --limit 50 --wait 45s --json       # bounded run, machine-readable
```

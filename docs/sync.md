# sync

Read when: running continuous capture, one-shot sync, contact/group refresh, or background media download.

`wacli sync` requires an existing authenticated store and never displays a QR code. It captures WhatsApp Web events into the local SQLite store.

## Command

```bash
wacli sync [--once] [--follow] [--idle-exit 30s] [--max-reconnect 5m] [--max-messages N] [--max-db-size SIZE] [--download-media] [--refresh-contacts] [--refresh-groups] [--events]
```

## Modes

- Default behavior follows continuously.
- `--once` exits after sync becomes idle.
- `--idle-exit` controls idle exit timing in once mode.
- `--max-reconnect 0` keeps reconnecting indefinitely.
- `--max-messages N` stops before storing more than `N` total messages locally.
- `--max-db-size SIZE` stops when `wacli.db` plus SQLite sidecars reaches `SIZE` (`500MB`, `2GB`, etc.).
- `--download-media` runs a bounded media downloader for sync events.
- `--refresh-contacts` imports contacts from the session store.
- `--refresh-groups` fetches joined groups live and updates the local DB.
- If neither storage cap is configured, sync prints one warning because WhatsApp history can grow the local database substantially.
- `WACLI_SYNC_MAX_MESSAGES` and `WACLI_SYNC_MAX_DB_SIZE` apply the same caps to `auth` bootstrap sync and `sync`.
- While `sync --follow` is running, `send text`, `send file`, `send voice`, and `send react` commands for the same store are delegated to the running sync process so they do not fail on the store lock.
- If whatsmeow reports an app-state LTHash mismatch, sync asks the primary device for the official recovery snapshot once for that app-state collection. If recovery also fails, the warning is printed and sync keeps handling normal message/history events.
- `--events` emits one NDJSON lifecycle event per stderr line for machine consumers. Routine human progress/status lines and interrupt prompts are suppressed while events are enabled.

## Examples

```bash
wacli sync --once
wacli sync --follow --max-reconnect 10m
wacli sync --follow --max-messages 250000 --max-db-size 2GB
wacli sync --once --refresh-contacts --refresh-groups
wacli sync --follow --download-media
wacli sync --once --events 2>events.ndjson
```

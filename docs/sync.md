# sync

Read when: running continuous capture, one-shot sync, contact/group refresh, or background media download.

`wacli sync` requires an existing authenticated store and never displays a QR code. It captures WhatsApp Web events into the local SQLite store.

## Command

```bash
wacli sync [--once] [--follow] [--idle-exit 30s] [--max-reconnect 5m] [--download-media] [--refresh-contacts] [--refresh-groups]
```

## Modes

- Default behavior follows continuously.
- `--once` exits after sync becomes idle.
- `--idle-exit` controls idle exit timing in once mode.
- `--max-reconnect 0` keeps reconnecting indefinitely.
- `--download-media` runs a bounded media downloader for sync events.
- `--refresh-contacts` imports contacts from the session store.
- `--refresh-groups` fetches joined groups live and updates the local DB.

## Examples

```bash
wacli sync --once
wacli sync --follow --max-reconnect 10m
wacli sync --once --refresh-contacts --refresh-groups
wacli sync --follow --download-media
```

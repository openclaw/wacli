# sync

Read when: running continuous capture, one-shot sync, contact/group refresh, or background media download.

`wacli sync` requires an existing authenticated store and never displays a QR code. It captures WhatsApp Web events into the local SQLite store.

## Command

```bash
wacli sync [--once] [--follow] [--idle-exit 30s] [--max-reconnect 5m] [--stale-threshold DURATION] [--max-messages N] [--max-db-size SIZE] [--download-media] [--refresh-contacts] [--refresh-groups] [--refresh-channels] [--events] [--webhook URL] [--webhook-secret SECRET]
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
- `--refresh-channels` fetches subscribed WhatsApp Channels live and updates local chat rows.
- `--webhook URL` posts successfully stored live message events as JSON on a bounded background worker.
- `--webhook-secret SECRET` signs webhook payloads with `X-Wacli-Signature: sha256=<hmac>`.
- Webhook delivery is best-effort: failures, request timeouts, and full-queue drops are logged as warnings and do not stop sync. Retries/backoff are intentionally out of scope for this flag.
- If neither storage cap is configured, sync prints one warning because WhatsApp history can grow the local database substantially.
- `WACLI_SYNC_MAX_MESSAGES` and `WACLI_SYNC_MAX_DB_SIZE` apply the same caps to `auth` bootstrap sync and `sync`.
- While `sync --follow` is running, `send text`, `send file`, `send sticker`, `send voice`, and `send react` commands for the same store are delegated to the running sync process so they do not fail on the store lock.
- After connecting, sync fetches WhatsApp chat app-state deltas (`regular_high` and `regular_low`) so starred, delete-for-me, mute, archive, pin, and mark-read changes made while `wacli` was offline are caught up instead of relying only on live push notifications.
- Sync imports messages sent from your other linked devices into the destination chat with `from_me=true`, so local history covers both incoming and outgoing conversation sides.
- If whatsmeow reports an app-state LTHash mismatch, sync asks the primary device for the official recovery snapshot once for that app-state collection. If recovery also fails, the warning is printed and sync keeps handling normal message/history events.
- Sync stores WhatsApp call signaling and call-log metadata in `call_events`; inspect it with `wacli calls list`.
- Sync stores WhatsApp status broadcasts in `status_messages`, separate from normal chat `messages`.
- In an interactive terminal, routine connected/history/progress updates share one updating stderr status line. Warnings and errors still print as separate lines so they remain visible.
- `--stale-threshold DURATION` in follow mode detects keepalive failures. If whatsmeow reports that the last successful keepalive is older than this duration, sync force-closes the connection and reconnects. Healthy quiet sessions are not reconnected just because no chat events arrive. Disabled by default (`0`).
- A `stale` NDJSON event is emitted when the threshold is exceeded, containing `threshold`, `idle_duration`, `error_count`, and `source` fields.
- While `sync --follow` is running, a `HEARTBEAT` file is written to the store directory (at most once per minute) with the last observed follow activity timestamp in RFC 3339 format. External watchdogs or `wacli doctor` can read this as a freshness signal; it is separate from keepalive health.
- `--events` emits one NDJSON lifecycle event per stderr line for machine consumers. Routine human progress/status lines, interrupt prompts, and command errors are emitted as events while events are enabled.

## Examples

```bash
wacli sync --once
wacli sync --follow --max-reconnect 10m
wacli sync --follow --stale-threshold 10m
wacli sync --follow --max-messages 250000 --max-db-size 2GB
wacli sync --once --refresh-contacts --refresh-groups --refresh-channels
wacli sync --follow --download-media
wacli sync --once --events 2>events.ndjson
wacli sync --follow --stale-threshold 5m --events 2>events.ndjson
wacli sync --follow --webhook https://example.com/wacli --webhook-secret "$WACLI_WEBHOOK_SECRET"
```

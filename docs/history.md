# history

Read when: trying to fetch older messages for a known chat.

`wacli history backfill` sends on-demand history sync requests to the primary device. This is best-effort and depends on the phone being online and WhatsApp returning older messages.

## Command

```bash
wacli history backfill --chat JID [--count 50] [--requests N] [--wait 1m] [--idle-exit 5s]
```

## Limits

- `--count` defaults to 50 and must be at most 500.
- `--requests` defaults to 1 and must be at most 100.
- Requests are per chat.
- The anchor is the oldest locally stored message in that chat.
- Automatic initial history-sync blob downloads are disabled during backfill; only on-demand responses are processed.

## Examples

```bash
wacli history backfill --chat 1234567890@s.whatsapp.net --requests 10 --count 50
wacli history backfill --chat 123456789@g.us --requests 3 --wait 90s
```

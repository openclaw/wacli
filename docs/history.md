# history

Read when: trying to fetch older messages for a known chat.

`wacli history` inspects local archive coverage and can send on-demand history sync requests to the primary device. Backfill is best-effort and depends on the phone being online and WhatsApp returning older messages.

## Commands

```bash
wacli history coverage [--query TEXT] [--kind KIND] [--include-blocked] [--only-actionable]
wacli history fill --dry-run [--query TEXT] [--kind KIND] [--limit 100]
wacli history backfill --chat JID [--count 50] [--requests N] [--wait 1m] [--idle-exit 5s] [--events]
```

## Coverage and planning

- `history coverage` reads only the local `wacli.db` store.
- `ready` chats have at least one local message, so `history backfill` has an anchor.
- `blocked` / `no_local_anchor` chats have no local message yet; run `wacli sync` first.
- `history fill --dry-run` lists matching ready chats that would be selected for a future multi-chat fill workflow. It does not connect to WhatsApp or write state.

## Limits

- `--count` defaults to 50 and must be at most 500.
- `--requests` defaults to 1 and must be at most 100. Each requested batch may make one extra attempt after a timeout.
- Requests are per chat.
- The anchor starts at the oldest locally stored message in that chat. If the phone does not answer within `--wait`, backfill retries once using the next chronological local message (timestamp, then row ID). No message IDs or content types are filtered out, and no local rows are deleted.
- Each attempt gets its own `--wait`; a batch can therefore wait up to twice that duration for responses. If there is no next anchor, or the retry also times out, backfill stops with an error naming the unanswered anchor. Transport errors and cancellation are not retried.
- A successful retry must add history older than the original local anchor to continue to another batch. Returning only already-stored messages stops backfill normally.
- Automatic initial history-sync blob downloads are disabled during backfill; only on-demand responses are processed.
- `--events` emits NDJSON request/response/stop lifecycle events on stderr. Requests include `anchor_msg_id`, and a `warning` with code `backfill_anchor_retry` identifies the unanswered anchor and its replacement. Human output reports the same retry on stderr. The result's request count includes retry attempts.

## Examples

```bash
wacli history coverage --include-blocked
wacli history coverage --query family --only-actionable
wacli history fill --dry-run --kind group --limit 20
wacli history backfill --chat 1234567890@s.whatsapp.net --requests 10 --count 50
wacli history backfill --chat 123456789@g.us --requests 3 --wait 90s
```

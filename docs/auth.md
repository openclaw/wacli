# auth

Read when: pairing a store, checking auth state, logging out, or choosing QR vs phone pairing.

`wacli auth` connects interactively and bootstraps sync after successful pairing. `wacli sync` never shows a QR code, so use `auth` first for a new store or named account.

## Commands

```bash
wacli auth [--follow] [--idle-exit 30s] [--download-media] [--qr-format terminal|text] [--phone PHONE] [--events]
wacli auth status
wacli auth logout
wacli --account work auth status
```

## Notes

- Default pairing prints a terminal QR code.
- `--qr-format text` prints the raw QR payload for external renderers.
- `--phone PHONE` uses WhatsApp phone-number pairing instead of QR pairing.
- `--history-days N` asks the primary device for the last N days of history instead of its whole recent window (about three months), and `--history-max-per-chat M` caps how many messages each chat brings. Both are sent in the pairing handshake, so they apply to this pairing only: they do nothing on a device that is already linked, and the primary decides whether to honour them. Use them when linking again to repair a gap, where the whole window would re-import messages you already hold.
- `--full-history` asks the primary device for a full history sync instead of its recent window of about three months; `--history-days N` then bounds how far back it goes. `--history-size-mb N` sets how many megabytes the primary may put into that full sync, which otherwise follows the primary's own default. Like the limits above, they travel in the pairing handshake and the primary decides what to send: a personal account was seen stopping at about 50 MB without `--history-size-mb`, and at three years back whatever `--history-days` asked.
- Transient websocket drops before pairing completes are retried with a fresh QR/code.
- Passkey-gated pairing is not yet supported. If WhatsApp requests passkey verification or confirmation, auth stops with an actionable error instead of continuing to rotate unusable QR codes.
- After pairing, auth runs bootstrap sync until idle unless `--follow` is set.
- Bootstrap sync honors `WACLI_SYNC_MAX_MESSAGES` and `WACLI_SYNC_MAX_DB_SIZE` to cap local history growth.
- `--events` emits NDJSON lifecycle events on stderr, including raw QR and phone-pairing codes for external renderers.
- `auth status` reports whether the local store is authenticated. A recorded remote logout overrides a stale device row until WhatsApp confirms a new login.
- `auth logout` invalidates the linked-device session and requires writable mode.
- For multiple accounts, prefer `wacli accounts add NAME`; it creates an isolated account store and runs the same auth/bootstrap flow.

## Examples

```bash
wacli auth
wacli auth --qr-format text
wacli auth --phone "+1 (234) 567-8900"
wacli auth --download-media
wacli auth --history-days 1
wacli auth --full-history --history-days 3650 --history-size-mb 2048
wacli auth status --json
wacli auth logout
```

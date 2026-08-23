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
- Transient websocket drops before pairing completes are retried with a fresh QR/code.
- Passkey-gated pairing is not yet supported. If WhatsApp requests passkey verification or confirmation, auth stops with an actionable error instead of continuing to rotate unusable QR codes.
- After pairing, auth runs bootstrap sync until idle unless `--follow` is set.
- Bootstrap sync honors `WACLI_SYNC_MAX_MESSAGES` and `WACLI_SYNC_MAX_DB_SIZE` to cap local history growth.
- `--events` emits NDJSON lifecycle events on stderr, including raw QR and phone-pairing codes for external renderers.
- `auth status` reports whether the local store is authenticated.
- `auth logout` invalidates the linked-device session and requires writable mode.
- For multiple accounts, prefer `wacli accounts add NAME`; it creates an isolated account store and runs the same auth/bootstrap flow.

## Examples

```bash
wacli auth
wacli auth --qr-format text
wacli auth --phone "+1 (234) 567-8900"
wacli auth --download-media
wacli auth status --json
wacli auth logout
```

For large accounts, `wacli auth --optimized` persists a bounded local index and
asks WhatsApp for at most 30 days and 50 messages per chat during initial
pairing. See [sync](sync.md#optimized-sync) for the retained-chat policy.

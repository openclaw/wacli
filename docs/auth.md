# auth

Read when: pairing a store, checking auth state, logging out, or choosing QR vs phone pairing.

`wacli auth` connects interactively and bootstraps sync after successful pairing. `wacli sync` never shows a QR code, so use `auth` first for a new store.

## Commands

```bash
wacli auth [--follow] [--idle-exit 30s] [--download-media] [--qr-format terminal|text] [--phone PHONE]
wacli auth status
wacli auth logout
```

## Notes

- Default pairing prints a terminal QR code.
- `--qr-format text` prints the raw QR payload for external renderers.
- `--phone PHONE` uses WhatsApp phone-number pairing instead of QR pairing.
- After pairing, auth runs bootstrap sync until idle unless `--follow` is set.
- `auth status` reports whether the local store is authenticated.
- `auth logout` invalidates the linked-device session and requires writable mode.

## Examples

```bash
wacli auth
wacli auth --qr-format text
wacli auth --phone "+1 (234) 567-8900"
wacli auth --download-media
wacli auth status --json
wacli auth logout
```

# profile

Read when: managing the authenticated WhatsApp profile or fetching profile metadata for a WhatsApp user.

`wacli profile` manages account-level WhatsApp profile settings for the linked account.

## Command

```bash
wacli profile set-picture <image>
wacli profile remove-picture
wacli profile set-about <text>
wacli profile set-name <name>
wacli profile picture-info --jid <jid-or-phone> [--preview] [--existing-id ID]
wacli profile get-about --jid <jid-or-phone>
wacli profile business --jid <jid-or-phone>
wacli profile business get [--jid <jid-or-phone>]
wacli profile business set [--description T] [--address T] [--email T] [--website URL]... [--hours-timezone TZ --hours day=spec]...
```

## Notes

- Mutating commands (`set-picture`, `remove-picture`, `set-about`, `set-name`) require authentication, a live connection, writable mode, and the store lock.
- Input can be JPEG or PNG.
- PNG transparency is flattened onto a white background before upload.
- Images larger than 640 px on either side are resized before upload.
- `set-picture` prints the picture ID returned by WhatsApp; use `--json` for machine-readable output.
- `set-about` updates the profile About text, not ephemeral status broadcasts. Use `send status` for WhatsApp status posts.
- `set-name` updates the WhatsApp push/display name through whatsmeow app-state support.
- `picture-info` fetches metadata and a CDN URL for the target's profile picture. Pass `--preview` for preview metadata or `--existing-id` to let WhatsApp report unchanged pictures.
- `get-about` fetches a target user's current About text.
- `business` fetches a target WhatsApp Business profile when available (legacy shape, through whatsmeow, without description or websites).
- `business get` fetches the full WhatsApp Business profile, as WhatsApp Web shows it: `description`, `address`, `email`, `websites`, `categories`, `business_hours_timezone`, `business_hours` (`day_of_week`, `mode`, and `open_time`/`close_time` as minutes from midnight) and `profile_options`. Without `--jid` it reads the linked account. Days missing from `business_hours` are closed.
- `business set` updates the linked account's own profile with the same delta mutation WhatsApp Web sends (`w:biz`, `business_profile v=3 mutation_type=delta`): only the flags you pass change, and an empty value clears a field. Limits are validated before anything is sent: description 512 characters, address 256, email 128 (a plain address), at most two websites of 256 characters starting with `http://` or `https://`. `--hours` entries replace the whole week (`mon=09:00-18:00`, `sat=open_24h`, `sun=appointment_only`; unlisted days are closed) and need `--hours-timezone` (IANA); `--hours-closed` closes every day. The command re-reads the profile and prints it after the update. Categories are read-only.
- `business get` and `business set` go through the running `wacli sync` process when it owns the store (send-socket kinds `business_profile_get`, `business_profile_set`), so the line keeps a single live connection; an older daemon answers `unsupported send kind`. Without a running sync they connect on their own like the other live profile commands. They are outside the message-send queue.
- Live fetch commands do not mutate WhatsApp profile settings, but they still open the WhatsApp session store and therefore cannot run with `--read-only`.

## Examples

```bash
wacli profile set-picture ./avatar.jpg
wacli profile set-picture ./avatar.png --json
wacli profile remove-picture
wacli profile set-about "Available today"
wacli profile set-name "Ops Desk"
wacli profile picture-info --jid +15551234567 --json
wacli profile get-about --jid 15551234567@s.whatsapp.net
wacli profile business --jid +15551234567 --json
wacli profile business get --json
wacli profile business set --description "Cameras and lenses" --website https://example.com --json
wacli profile business set --hours-timezone America/Lima --hours mon=09:00-18:00 --hours sat=10:00-14:00
wacli catalog list --json
wacli catalog list --jid +15551234567 --limit 100 --after <next_cursor>
```

## Catalog

`wacli catalog list` reads a WhatsApp Business catalog, the linked account's by default: `has_catalog`, and per product `id`, `name`, `description`, `retailer_id`, `url`, `price_amount_1000` (the amount as WhatsApp sends it, in thousandths of the currency unit), `price` (two decimals), `sale_price_amount_1000`/`sale_price` when there is a sale, `currency`, `image_url` (100 px thumbnail), `original_image_url`, `hidden`, `review_status`, `availability` and `max_available`. `--limit` is 1-200 (default 50); `next_cursor` is non-empty when there are more pages, pass it to `--after`.

WhatsApp no longer answers the `w:biz:catalog` IQ over the socket (it stays silent until the timeout, WhiskeySockets/Baileys#2717), so the command does what WhatsApp Web does: a public HTTPS GraphQL query to `graph.whatsapp.com/graphql/catalog` with the app token shipped in WhatsApp Web's bundle. It needs no session and never involves the running sync; without `--jid` it only reads the linked JID from the store, so it works with `--read-only`. `has_catalog: false` means WhatsApp knows no catalog for that account.

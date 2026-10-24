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
- While `wacli sync --follow` holds the store lock, `picture-info` asks the running sync process instead of failing: the lookup uses its live connection, takes no send slot and is not paced. The output is the same. An older sync process answers `unsupported send kind "profile_picture_info"`; restart it after upgrading.
- Integrations can send the same request on the sync process's delegation socket: `{"version":1,"kind":"profile_picture_info","jid":"<user, LID or group JID>","preview":false,"existing_id":"","timeout_ms":15000}` (omit `jid` for the linked account). A success carries `profile_picture` (`jid`, `url`, `id`, `type`, `directPath`, `hash`, or `unchanged: true`); a failure carries `error` and a stable `error_code`: `not_set`, `hidden`, `invalid_jid`, `timeout` (the caller's deadline or WhatsApp's own query timeout; retry), `unavailable` (no linked account, or sync is reconnecting; wait and retry) or `lookup_failed` (any other failure; `error` carries its cause).
- `get-about` fetches a target user's current About text.
- `business` fetches a target WhatsApp Business profile when available.
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
```

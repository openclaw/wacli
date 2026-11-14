# calls

Read when: listing WhatsApp call events captured by sync.

`wacli calls` reads call metadata from the local store. It does not place, accept, or reject calls.

## Commands

```bash
wacli calls list [--chat JID] [--asc] [--limit N] [--after DATE] [--before DATE]
```

## Data Model

- Live WhatsApp call signaling events are stored separately from normal messages.
- Historical WhatsApp call-log messages are stored as normal message rows and as structured `call_events` rows.
- JSON output includes `chat_jid`, `call_id`, `event_type`, `direction`, `media`, `outcome`, `duration_secs`, `timestamp`, and participant metadata when WhatsApp provides it.
- When sync resolves a hidden-user (LID) identity to a phone number, existing call events move to the phone-number chat and caller identities are updated, including group calls. Duplicate events retain the phone-number record and fill its missing metadata. This cannot restore call records already lost by an earlier version.
- Time filters accept RFC3339 or `YYYY-MM-DD`.

## Examples

```bash
wacli calls list --limit 20
wacli calls list --chat 1234567890@s.whatsapp.net --json
wacli calls list --after 2026-05-01 --asc
```

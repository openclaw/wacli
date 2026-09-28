# presence

Read when: sending typing, recording, or paused chat indicators, or checking whether a contact is online.

`wacli presence` sends ephemeral WhatsApp chat-state updates and asks for a contact's online state. It does not send a message.

## Commands

```bash
wacli presence typing --to PHONE_OR_JID [--media audio]
wacli presence paused --to PHONE_OR_JID
wacli presence subscribe --to PHONE_OR_JID [--wait 10s]
```

## Notes

- `typing` sends a composing indicator.
- `typing --media audio` sends a recording indicator.
- `paused` clears the composing indicator.
- `subscribe` asks WhatsApp to send the contact's presence (online, last seen) to this device and prints the first answer: online, last seen at a time, offline with the last seen withheld, or no answer within `--wait`.
- WhatsApp applies the contact's privacy settings. A contact who hides their online status or last seen from you sends no answer or no time, and an account that hides its own last seen does not see anyone else's.
- WhatsApp sends presence only to devices that show as available. Without a running sync, `subscribe` connects, shows as online while it waits, then sends unavailable and disconnects; the subscription ends with that connection.
- While `sync --follow` runs, `subscribe` is delegated to it, like sends: the subscription is made on the sync's connection, renewed after every reconnect, and with `sync --events` each change arrives as a `presence` event, and the contact's typing as a `chat_presence` event (see [sync](sync.md)). `--wait 0` returns as soon as the subscription is made. A sync started with `--presence-mode quiet` refuses it, since WhatsApp would send it nothing.
- Recipients accept phone numbers with common formatting or JIDs. Only users have a presence: groups are rejected.

## Examples

```bash
wacli presence typing --to 1234567890
wacli presence typing --to 1234567890 --media audio
wacli presence paused --to 1234567890
wacli presence subscribe --to 1234567890
wacli presence subscribe --to 1234567890 --wait 0 --json   # with sync --follow --events running
```

# presence

Read when: sending typing, recording, or paused chat indicators.

`wacli presence` sends ephemeral WhatsApp chat-state updates. It does not send a message.

## Commands

```bash
wacli presence typing --to PHONE_OR_JID [--media audio]
wacli presence paused --to PHONE_OR_JID
```

## Notes

- `typing` sends a composing indicator.
- `typing --media audio` sends a recording indicator.
- `paused` clears the composing indicator.
- Recipients accept phone numbers with common formatting or JIDs.

## Examples

```bash
wacli presence typing --to 1234567890
wacli presence typing --to 1234567890 --media audio
wacli presence paused --to 1234567890
```

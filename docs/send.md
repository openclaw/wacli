# send

Read when: sending text, files, quoted replies, or reactions.

`wacli send` requires authentication, a live connection, and writable mode. Send attempts are bounded and retry once after reconnect for known stale-session/usync timeout failures.

## Commands

```bash
wacli send text --to RECIPIENT --message TEXT [--pick N] [--reply-to MSG_ID] [--reply-to-sender JID]
wacli send file --to RECIPIENT --file PATH [--pick N] [--caption TEXT] [--filename NAME] [--mime TYPE] [--reply-to MSG_ID] [--reply-to-sender JID]
wacli send react --to PHONE_OR_JID --id MSG_ID [--reaction TEXT] [--sender JID]
```

## Recipients

- `send text` and `send file` accept a JID, phone number, or synced contact/group/chat name.
- If a name matches multiple recipients, interactive terminals prompt.
- In scripts, use `--pick N` to choose a displayed match.
- Phone numbers may use common formatting such as `+1 (234) 567-8900`.

## Replies and reactions

- `--reply-to` quotes a stored message ID.
- For unsynced group replies, pass `--reply-to-sender`.
- `send react` defaults to thumbs-up.
- Pass `--reaction ""` to clear a reaction.
- For group reactions, pass `--sender` for the original message sender.

## Files

- File uploads are capped at 100 MiB.
- MIME type is detected automatically unless `--mime` is set.
- `--filename` changes the displayed document name.
- Captions apply to images, videos, and documents.

## Examples

```bash
wacli send text --to mom --message "landed"
wacli send text --to "Family" --pick 2 --message "on my way"
wacli send text --to 1234567890 --message "replying" --reply-to ABC123
wacli send file --to 1234567890 --file ./pic.jpg --caption "hi"
wacli send file --to 1234567890 --file /tmp/report --filename report.pdf
wacli send react --to 1234567890 --id ABC123 --reaction "❤️"
```

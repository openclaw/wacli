# send

Read when: sending text, files, stickers, quoted replies, or reactions.

`wacli send` requires authentication, a live connection, and writable mode. Send attempts are bounded and retry once after reconnect for known stale-session/usync timeout failures. `Sent to ...` and JSON `sent: true` mean WhatsApp accepted the send request and returned a message ID; they do not confirm recipient delivery. After a successful send, wacli keeps the connection alive briefly so whatsmeow can handle retry receipts from devices that could not decrypt the first copy. Repeated send commands within 5 seconds print a stderr warning so tight loops make WhatsApp rate-limit/account-risk visible.

When `sync --follow` is already running for the same store, send commands delegate the send to that running process instead of opening a second WhatsApp session. This keeps scripts usable while continuous sync owns the store lock.

## Commands

```bash
wacli send text --to RECIPIENT --message TEXT [--message-escapes] [--pick N] [--mention USER] [--no-preview] [--reply-to MSG_ID] [--reply-to-sender JID] [--post-send-wait 2s]
wacli send file --to RECIPIENT --file PATH [--pick N] [--caption TEXT] [--filename NAME] [--mime TYPE] [--ptt] [--reply-to MSG_ID] [--reply-to-sender JID] [--post-send-wait 2s]
wacli send sticker --to RECIPIENT --file PATH [--pick N] [--reply-to MSG_ID] [--reply-to-sender JID] [--post-send-wait 2s]
wacli send voice --to RECIPIENT --file PATH [--pick N] [--mime TYPE] [--reply-to MSG_ID] [--reply-to-sender JID] [--post-send-wait 2s]
wacli send react --to PHONE_OR_JID --id MSG_ID [--reaction TEXT] [--sender JID] [--post-send-wait 2s]
```

## Recipients

- `send text`, `send file`, `send sticker`, and `send voice` accept a JID, phone number, or synced contact/group/chat name.
- Channel JIDs use `...@newsletter`; `send text` and `send file` can target channels when the authenticated account has posting permission.
- If a name matches multiple recipients, interactive terminals prompt.
- In scripts, use `--pick N` to choose a displayed match.
- Phone numbers may use common formatting such as `+1 (234) 567-8900`.

## Replies and reactions

- `send text` fetches Open Graph metadata for the first `http://` or `https://` URL and sends it as a WhatsApp link preview.
- Preview metadata fetches time out after 10 seconds and fall back to plain text.
- Pass `--no-preview` to disable link-preview fetching.
- `--message` is literal by default. Pass `--message-escapes` to interpret `\n`, `\r`, `\t`, `\\`, and `\"` before sending.
- Use repeatable `--mention USER` with a phone number or user JID to add WhatsApp mentions to `send text`.
- `--reply-to` quotes a stored message ID.
- For unsynced group replies, pass `--reply-to-sender`.
- `send react` defaults to thumbs-up.
- Pass `--reaction ""` to clear a reaction.
- Sent reactions are stored locally immediately, including reaction target and display text.
- For group reactions, pass `--sender` for the original message sender.
- Use `--post-send-wait 0` to disable the retry-receipt grace window for latency-sensitive scripts.

## Files

- File uploads are capped at 100 MiB.
- MIME type is detected automatically unless `--mime` is set.
- `--filename` changes the displayed document name.
- Captions apply to images, videos, and documents.
- Files sent to channels use WhatsApp's unencrypted newsletter media upload path and include the upstream media handle required by `whatsmeow`.
- Quoted file replies and `--ptt` voice-note mode are not supported for channel sends.
- `send sticker` requires 512x512 WebP input. Static stickers are capped at 100 KiB; animated stickers are capped at 500 KiB and are sent with animation metadata.
- `send voice` is a shortcut for `send file --ptt`.
- Voice notes require OGG/Opus audio (`audio/ogg; codecs=opus`).
- When available, `ffprobe` sets voice-note duration and `ffmpeg` generates the 64-sample waveform from decoded PCM audio.

## Examples

```bash
wacli send text --to mom --message "landed"
wacli send text --to mom --message "line1\nline2" --message-escapes
wacli send text --to "Family" --pick 2 --message "on my way"
wacli send text --to "Family" --message "hey @15551234567" --mention +15551234567
wacli send text --to 1234567890 --message "replying" --reply-to ABC123
wacli send file --to 1234567890 --file ./pic.jpg --caption "hi"
wacli send file --to 1234567890 --file /tmp/report --filename report.pdf
wacli send sticker --to 1234567890 --file ./sticker-512.webp
wacli send voice --to 1234567890 --file ./voice.ogg
wacli send react --to 1234567890 --id ABC123 --reaction "❤️"
```

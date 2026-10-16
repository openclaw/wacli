# stickers

Read when: listing or downloading your favorite and recent WhatsApp stickers.

`wacli stickers` reads the account's sticker lists from the local store and downloads their files.

## Commands

```bash
wacli stickers list [--favorites] [--recent] [--limit N]
wacli stickers download [--favorites] [--recent] [--hash FILE_HASH]... [--output DIR]
wacli stickers refresh
```

## Data Model

- **Favorites** live in WhatsApp's synced app state (`favoriteSticker` in the `regular_low` collection). Sync stores every change, including unfavoriting, as it arrives; a newer change always wins over an older one replayed later.
- **Recent stickers** are sent to a linked device only once, in the history sync at pairing. Removing one from recents on the phone is synced; stickers used later are not, so the recent list stops growing after pairing.
- Installed sticker packs are not synced to linked devices.
- Each sticker is identified by `file_hash`, the base64 SHA-256 of its file, the same identity WhatsApp uses.
- JSON output includes `file_hash`, `favorite`, `favorited_at`, `recent`, `recent_weight`, `last_sent_at`, `mime_type`, dimensions, `is_lottie`, `is_avatar` and `downloadable`. Download keys and paths are never printed.

## list

Lists favorites first, newest first, then recent stickers by weight. `--favorites` or `--recent` selects one list; both or neither list everything.

## download

Downloads each sticker directly from WhatsApp's media servers and verifies it against its `file_hash`. Files are named `<sha256 hex>.webp` (`.was` for animated Lottie stickers). Files already present and verified are reported as `cache` and not fetched again.

### Notes

- Takes no store lock and opens no WhatsApp connection, so it runs next to `sync --follow`.
- `--output` is required in `--read-only` mode; otherwise files go under the store's `media/stickers` directory.
- A sticker whose server copy expired reports its error and the run continues. JSON reports `downloaded`, `cached` and `failed` counts, plus each result. Each failure is also a `warning` on stderr, a `sticker_download_failed` event with `--events`.

## refresh

Stores favorites saved before this store recorded them. It replays the full `regular_low` app-state collection, the same replay wacli uses to recover app state, so archive, pin and read state in that collection are re-applied too. Favorites changed afterwards arrive during sync without it.

### Notes

- Run it once after upgrading. Like `groups refresh`, it connects with the store lock; with a running `sync --follow`, restart it with `--refresh-stickers` instead, which runs the same replay at sync start.
- If the replay fails, the request stays recorded and the next `wacli sync` retries it.
- Prints `Imported N favorite stickers.`; JSON reports `stickers`.
- On accounts whose full replays are known not to verify, wacli asks the phone for a recovery snapshot instead and waits for it, up to five minutes. If it does not arrive, the refresh fails and the request stays recorded for the next sync.
- Not available in `--read-only` mode.

## Examples

```bash
wacli stickers refresh
wacli sync --follow --refresh-stickers
wacli stickers list --favorites
wacli stickers list --json
wacli stickers download --favorites --output ./stickers
wacli --read-only stickers download --output /tmp/stickers
```

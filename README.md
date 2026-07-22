# 🗃️ wacli — WhatsApp CLI: sync, search, send

![wacli banner](docs/assets/readme-banner.jpg)

A scriptable WhatsApp client built on [`whatsmeow`](https://github.com/tulir/whatsmeow). Pairs as a linked WhatsApp Web device, mirrors your messages into a local SQLite store, and gives you offline search, sending, and chat/group/contact management from the command line.

> Third-party tool. Uses the WhatsApp Web protocol via `whatsmeow`. Not affiliated with WhatsApp.

Full documentation: **<https://wacli.sh>**

## Features

- **Auth + sync** — QR pairing, one-shot or follow-mode sync, optional media downloads, optional signed webhook fan-out.
- **Offline message store** — SQLite with FTS5 search (LIKE fallback), filterable by chat, sender, direction, time, and media type, with status broadcasts stored separately.
- **Sending** — text with mentions/replies/link-previews, files (image/video/audio/document, ≤100 MiB), stickers, voice notes, reactions, and status broadcasts; rapid-send guardrails and retry-receipt grace.
- **History backfill** — best-effort per-chat requests to your primary device for older messages.
- **Contacts / chats / groups / channels / profile** — search, alias, tag, archive, pin, mute, mark-read, rename, prune, manage participants and invite links, send to channels, and manage profile metadata.
- **Diagnostics + safety** — `doctor`, read-only mode, store locks with owner reporting, panic recovery, bounded media queue, owner-only DB perms.
- **Scriptable** — `--json` everywhere, `--events` NDJSON lifecycle stream, deterministic exit codes.

## Install

### Homebrew (recommended)

```bash
brew install openclaw/tap/wacli
```

If a Linux install reports `Binary was compiled with 'CGO_ENABLED=0'`, run `brew update && brew reinstall openclaw/tap/wacli`.

### Build from source

`wacli` requires Go 1.26.5 or newer and uses `go-sqlite3`, so cgo + a C compiler are required.

- macOS: Xcode Command Line Tools.
- Debian/Ubuntu: `sudo apt install build-essential`.

```bash
CGO_ENABLED=1 CGO_CFLAGS="-Wno-error=missing-braces" \
  go install -tags sqlite_fts5 github.com/openclaw/wacli/cmd/wacli@latest
```

For local development:

```bash
git clone https://github.com/openclaw/wacli.git
cd wacli
CGO_ENABLED=1 CGO_CFLAGS="-Wno-error=missing-braces" \
  go build -tags sqlite_fts5 -o ./dist/wacli ./cmd/wacli
./dist/wacli --help
```

### Docker

```bash
docker build -t wacli .
docker run --rm -it -v "$PWD/.wacli:/data" wacli auth
docker run --rm -v "$PWD/.wacli:/data" wacli sync --follow
```

The image keeps WhatsApp auth, SQLite, config, and cache under `/data`; it also includes `ffmpeg` for media helpers.

## Quick start

```bash
# 1. Pair (shows QR), then bootstrap sync
wacli auth

# 2. Keep syncing in the background (no QR; needs prior auth)
wacli sync --follow

# 3. Search
wacli messages search "meeting"

# 4. Send
wacli send text --to 1234567890 --message "hello"
wacli send file --to mom --file ./pic.jpg --caption "hi"
wacli send status --message "available today" --background-color '#1f7a8c'

# 5. Diagnostics
wacli doctor
```

Recipients accept a JID, phone number (E.164 or formatted), channel JID, or a synced contact/group/chat name. Ambiguous names prompt in a TTY; pass `--pick N` in scripts.

More recipes — replies, mentions, stickers, voice, reactions, statuses, channels, history backfill, chat management — live in the [docs](https://wacli.sh).

## Documentation

| Area | Pages |
| --- | --- |
| **Setup** | [overview](docs/overview.md) · [auth](docs/auth.md) · [accounts](docs/accounts.md) · [sync](docs/sync.md) · [doctor](docs/doctor.md) |
| **Messaging** | [messages](docs/messages.md) · [calls](docs/calls.md) · [send](docs/send.md) · [media](docs/media.md) · [presence](docs/presence.md) |
| **Address book** | [contacts](docs/contacts.md) · [chats](docs/chats.md) · [groups](docs/groups.md) · [channels](docs/channels.md) |
| **History** | [history coverage / fill / backfill](docs/history.md) |
| **Local store** | [store](docs/store.md) · [companion integrations](docs/integrations.md) |
| **Misc** | [profile](docs/profile.md) · [version](docs/version.md) · [completion](docs/completion.md) · [release](docs/release.md) |

## Configuration

Default store: `~/.local/state/wacli` on Linux, `~/.wacli` elsewhere. Existing `~/.wacli` directories on Linux keep working. Use `wacli accounts add NAME` and `--account NAME` for first-class multi-account stores.

**Global flags:** `--store DIR`, `--account NAME`, `--json`, `--events`, `--full`, `--timeout DUR`, `--lock-wait DUR`, `--read-only`.

**Environment overrides:**

| Variable | Effect |
| --- | --- |
| `WACLI_STORE_DIR` | Default store directory. |
| `WACLI_READONLY` | `1`/`true`/`yes`/`on` enables read-only mode. |
| `WACLI_DEVICE_LABEL` | Linked-device label shown in WhatsApp. Defaults to `wacli - <OS> (<host>)`. |
| `WACLI_DEVICE_PLATFORM` | Linked-device platform. Defaults to `DESKTOP`; invalid values fall back to `CHROME`. |
| `WACLI_SYNC_MAX_MESSAGES` | Stop sync once total local messages exceed this count. |
| `WACLI_SYNC_MAX_DB_SIZE` | Stop sync once `wacli.db` + sidecars reach a size like `500MB` or `2GB`. |

## Backfilling older history

`wacli sync` only stores what WhatsApp Web sends opportunistically. To fetch *older* messages, `wacli` issues on-demand history requests to your **primary device** (your phone), which must be online.

- Best-effort: WhatsApp may not return full history.
- One request anchors on the **oldest locally stored message** in that chat — run `sync` first.
- Recommended `--count 50` per request (max 500). Max `--requests 100` per run.
- `history coverage` shows which chats are eligible. `history fill --dry-run` plans without connecting.

```bash
wacli history coverage --include-blocked
wacli history fill --dry-run --kind group --limit 20
wacli history backfill --chat 1234567890@s.whatsapp.net --requests 10 --count 50
```

Loop over every known chat:

```bash
wacli --json chats list --limit 100000 \
  | jq -r '.data[].JID' \
  | while read -r jid; do
      wacli history backfill --chat "$jid" --requests 3 --count 50
    done
```

## Credits

Heavily inspired by [`whatsapp-cli`](https://github.com/vicentereig/whatsapp-cli) by Vicente Reig.

## Maintainers

- Created by [@steipete](https://github.com/steipete)
- Currently maintained by [@dinakars777](https://github.com/dinakars777)

## License

See [`LICENSE`](LICENSE).

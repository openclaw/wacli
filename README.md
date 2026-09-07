# wacli 🗃️ — WhatsApp from your terminal

![wacli banner](docs/assets/readme-banner.jpg)

[![CI](https://img.shields.io/github/actions/workflow/status/openclaw/wacli/ci.yml?branch=main&style=flat-square&label=ci)](https://github.com/openclaw/wacli/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/openclaw/wacli?style=flat-square)](https://github.com/openclaw/wacli/releases/latest)
[![Platforms](https://img.shields.io/badge/platforms-macOS%20%7C%20Linux%20%7C%20Windows-blue?style=flat-square)](https://github.com/openclaw/wacli/releases/latest)
[![License](https://img.shields.io/github/license/openclaw/wacli?style=flat-square)](LICENSE)
[![Homebrew](https://img.shields.io/badge/Homebrew-openclaw%2Ftap-orange?style=flat-square)](https://github.com/openclaw/homebrew-tap)
[![Docs](https://img.shields.io/badge/docs-wacli.sh-blue?style=flat-square)](https://wacli.sh)

`wacli` is a scriptable WhatsApp client for people and tools that work from the command line. It pairs as a linked device, mirrors messages into a local SQLite store, and supports search, sending, and chat management.

> `wacli` uses the WhatsApp Web protocol through [`whatsmeow`](https://github.com/tulir/whatsmeow). It is not affiliated with WhatsApp or Meta.

## Install

Homebrew on macOS or Linux:

```sh
brew install openclaw/tap/wacli
```

Prebuilt archives for macOS, Linux, and Windows are available from [GitHub Releases](https://github.com/openclaw/wacli/releases/latest).

To build from source, install Go 1.27.0 or newer and a C compiler, then run:

```sh
CGO_ENABLED=1 CGO_CFLAGS="-Wno-error=missing-braces" \
  go install -tags sqlite_fts5 github.com/openclaw/wacli/cmd/wacli@latest
```

See the [installation guide](docs/install.md) for release archives, Docker, and platform-specific build requirements.

## Quick start

Pair the CLI by scanning the terminal QR code from WhatsApp's **Linked devices** screen. `auth` performs the first sync after pairing.

```sh
wacli auth
wacli messages search "meeting"
wacli send text --to +15551234567 --message "hello"
```

Sending requires a recipient you are allowed to contact. Recipients can be phone numbers, WhatsApp JIDs, or synced contact, group, and chat names.

The [quickstart](docs/quickstart.md) covers phone-number pairing, named accounts, media, and diagnostics.

## Keep messages in sync

Run a continuous sync to keep the local store current:

```sh
wacli sync --follow
```

`wacli` keeps the WhatsApp session and its own searchable message index in separate SQLite databases. Search reads the local index, so it works without a live WhatsApp connection:

```sh
wacli messages search "invoice" --has-media
wacli --json messages list --limit 20
```

WhatsApp Web provides history on a best-effort basis. Use [`history coverage`](docs/history.md) to inspect what is available locally before requesting older messages from the primary phone.

## Use wacli from scripts

Human-readable tables are the default. Use `--json` for one-shot commands and `--events` for NDJSON lifecycle events from long-running commands. Progress and errors stay on stderr.

Use `--read-only` or `WACLI_READONLY=1` when an integration must not change WhatsApp or the local store:

```sh
wacli --read-only --json messages search "invoice"
WACLI_READONLY=1 wacli --json doctor
```

Write commands take a per-store lock. When `sync --follow` owns that lock, supported send commands are delegated to the running sync process. See [companion integrations](docs/integrations.md) for webhooks and safe read-only SQLite access.

## Commands

| Area | What it covers |
| --- | --- |
| [`auth`](docs/auth.md), [`accounts`](docs/accounts.md) | Pair a linked device and manage isolated account stores. |
| [`sync`](docs/sync.md), [`history`](docs/history.md) | Mirror new events and request older per-chat history. |
| [`messages`](docs/messages.md), [`calls`](docs/calls.md) | Search, inspect, export, and manage local records. |
| [`send`](docs/send.md), [`media`](docs/media.md) | Send text and files or download synced media. |
| [`contacts`](docs/contacts.md), [`chats`](docs/chats.md) | Find people and manage local or remote chat state. |
| [`groups`](docs/groups.md), [`channels`](docs/channels.md) | Inspect and manage groups, communities, and channels. |
| [`profile`](docs/profile.md), [`presence`](docs/presence.md) | Manage profile details and chat presence. |
| [`store`](docs/store.md), [`doctor`](docs/doctor.md) | Inspect local storage and diagnose the setup. |

The complete documentation is at [wacli.sh](https://wacli.sh), or run `wacli help <command>` for the installed command reference.

## Configuration

The default store is `~/.local/state/wacli` on Linux and `~/.wacli` elsewhere. Override it with `--store DIR` or `WACLI_STORE_DIR`; use named accounts when each WhatsApp identity needs its own session, database, and lock.

```sh
wacli accounts add work
wacli --account work sync --follow
```

See [accounts](docs/accounts.md) for store selection and [sync](docs/sync.md) for storage limits, media downloads, webhooks, and presence behavior.

## Development

Development uses the Go 1.27.1 toolchain selected by `go.mod`, Node.js 24 or newer, pnpm, cgo, and a C compiler. The source minimum remains Go 1.27.0.

```sh
pnpm install --frozen-lockfile
pnpm build
pnpm format:check && pnpm lint && pnpm test
```

## Credits

Heavily inspired by [`whatsapp-cli`](https://github.com/vicentereig/whatsapp-cli) by Vicente Reig.

## Maintainers

- Created by [@steipete](https://github.com/steipete)
- Currently maintained by [@dinakars777](https://github.com/dinakars777)

## License

[MIT](LICENSE).

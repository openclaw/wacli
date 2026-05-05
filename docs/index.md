---
title: Overview
permalink: /
description: "wacli is a single Go CLI that pairs as a linked WhatsApp Web device, mirrors message history into local SQLite with FTS5 search, and exposes send, media, contact, and group workflows for terminals, scripts, and coding agents."
---

# wacli

A script-friendly WhatsApp CLI built on [`whatsmeow`](https://github.com/tulir/whatsmeow). One binary pairs as a linked WhatsApp Web device, syncs messages into a local SQLite store, and exposes search, send, media, contact, and group commands with predictable output for terminals, shell pipelines, and coding agents.

## Why wacli

- **Local mirror, fast search.** All synced messages land in a SQLite store with an FTS5 index; offline `messages search` returns hits in milliseconds.
- **Chat state controls.** Archive, pin, mute, and mark chats read/unread from the CLI, then filter `chats list` by those states.
- **Stable output.** Human-readable tables by default, `--json` to stdout for scripts, NDJSON `--events` for long-running commands. Human progress, prompts, and errors stay on stderr so pipes stay clean.
- **Single binary.** No daemon, no plugin host. Run `wacli auth`, then `wacli sync --follow` to keep the store warm.
- **Built for agents.** `--read-only` (or `WACLI_READONLY=1`) blocks every command that mutates WhatsApp or local state. Store locks prevent two instances from racing on the same device identity.
- **Boundable storage.** `sync` warns when storage is uncapped; `--max-messages` / `--max-db-size` cap local growth. Send retries are bounded; media uploads/downloads cap at 100 MiB.
- **Best-effort history.** `history backfill` requests older messages per chat from your primary device; documented as best-effort because WhatsApp Web is.

## Pick your path

- **Trying it.** Read [Install](install.md), then [Quickstart](quickstart.md). Pair, sync, and send your first message in under five minutes.
- **Searching old chats.** Read [Sync](sync.md) for the sync model and [History](history.md) for on-demand backfill.
- **Managing chat state.** Read [Chats](chats.md) for archive, pin, mute, and read/unread commands.
- **Sending from scripts.** Read [Send](send.md) for recipient resolution, replies, mentions, files, and reactions.
- **Wiring up an agent.** Pair `--read-only`, `--json`, and `--events` from [Overview](overview.md); read [Doctor](doctor.md) for self-checks.
- **Building companion tools.** Read [Companion integrations](integrations.md) for safe read-only SQLite and JSON integration patterns.
- **Looking up a flag.** Open the per-command pages from [Overview](overview.md).

## Status

Core implementation is in place. The [CHANGELOG](https://github.com/steipete/wacli/blob/main/CHANGELOG.md) tracks shipped behavior. WhatsApp Web is not a published API; expect occasional breakage from upstream protocol changes — `wacli` follows `whatsmeow` upstream.

## Out of scope

- Guaranteed full-history export (WhatsApp Web history is best-effort).
- A daemon, MCP server, web UI, or GUI.
- End-to-end "contact creation" inside WhatsApp; local aliases and tags only.

## Disclaimer

`wacli` is a third-party tool that uses the WhatsApp Web protocol via `whatsmeow`. It is **not affiliated with WhatsApp or Meta**. Use at your own risk; pairing as a linked device is subject to WhatsApp's terms.

Released under the [MIT license](https://github.com/steipete/wacli/blob/main/LICENSE).

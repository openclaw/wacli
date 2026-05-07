---
title: Install
description: "Install wacli via Homebrew tap, prebuilt release archives, or a local build with cgo."
---

# Install

`wacli` ships as a single binary. Local builds need cgo (because of `go-sqlite3` with FTS5); release artifacts and the Homebrew tap take care of that for you.

## Homebrew (macOS, Linux)

```bash
brew install steipete/tap/wacli
wacli --version
```

If a Linux install from the tap reports `Binary was compiled with 'CGO_ENABLED=0'`, update the tap and reinstall the formula:

```bash
brew update
brew reinstall steipete/tap/wacli
```

## GitHub releases (raw binaries)

Download the matching archive from the [latest release](https://github.com/openclaw/wacli/releases) and put `wacli` (or `wacli.exe` on Windows) on your `PATH`.

## Build from source

`wacli` uses `go-sqlite3`, so source builds require cgo and a C toolchain:

- macOS: Xcode Command Line Tools.
- Debian / Ubuntu: `sudo apt install build-essential`.
- Fedora / RHEL: `sudo dnf groupinstall "Development Tools"`.

Then:

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
./dist/wacli --version
```

The `sqlite_fts5` build tag is required for `messages search` to use the FTS5 index. Without it, search falls back to `LIKE`.

GCC 15 has stricter brace-init warnings; the `-Wno-error=missing-braces` flag keeps the `go-sqlite3` build green there. macOS / clang and older GCC do not need it.

If you have `pnpm` installed, `pnpm build` runs the same command and writes `./dist/wacli`.

## Verify the install

```bash
wacli --version
wacli doctor
wacli --help
```

`wacli doctor` checks the store directory, database integrity, FTS5 availability, and (with `--connect`) live connectivity to WhatsApp. See [Doctor](doctor.md).

## Updating

- **Homebrew tap**: `brew upgrade wacli` (or `brew reinstall steipete/tap/wacli`).
- **GitHub release archives**: download the new tarball / ZIP and replace the binary.
- **Source builds**: `git pull && pnpm build` (or the manual `go build` above). Local builds use the version compiled into the source tree; release artifacts inject the tag during GoReleaser builds.

The local store format is forward-compatible across point releases; routine upgrades do not require re-pairing.

## Storage

- Default store directory: `~/.local/state/wacli` on Linux (XDG state dir), `~/.wacli` on macOS / Windows. Existing Linux `~/.wacli` directories keep working.
- Override with `--store DIR` or `WACLI_STORE_DIR`.
- The store contains `session.db` (whatsmeow keys), `wacli.db` (messages + FTS), `media/`, and a `LOCK` file. See [Spec](spec.md#storage-layout) for the layout.
- Permissions are owner-only (`0700` on the directory, `0600` on files). Do not relax these — they protect your WhatsApp session keys.

## Related pages

- [Quickstart](quickstart.md) — pair, sync, and send your first message.
- [Auth](auth.md) — `wacli auth`, `auth status`, `auth logout`.
- [Sync](sync.md) — bootstrap and follow-mode sync, refresh flags.
- [Doctor](doctor.md) — self-checks and connectivity probe.
- [Release](release.md) — release workflow and artifact expectations.

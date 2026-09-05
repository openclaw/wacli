---
title: Install
description: "Install wacli via Homebrew tap, prebuilt release archives, or a local build with cgo."
---

# Install

`wacli` ships as a single binary. Local builds need cgo (because of `go-sqlite3` with FTS5); release artifacts and the Homebrew tap take care of that for you.

## Homebrew (macOS, Linux)

```bash
brew install openclaw/tap/wacli
wacli --version
```

If a Linux install from the tap reports `Binary was compiled with 'CGO_ENABLED=0'`, update the tap and reinstall the formula:

```bash
brew update
brew reinstall openclaw/tap/wacli
```

## GitHub releases (raw binaries)

Download the matching archive from the [latest release](https://github.com/openclaw/wacli/releases) and put `wacli` (or `wacli.exe` on Windows) on your `PATH`.

## Build from source

`wacli` requires Go 1.27.0 or newer and uses `go-sqlite3`, so source builds require cgo and a C toolchain:

- macOS: Xcode Command Line Tools.
- Debian / Ubuntu: `sudo apt install build-essential`.
- Fedora / RHEL: `sudo dnf groupinstall "Development Tools"`.

Then:

```bash
CGO_ENABLED=1 CGO_CFLAGS="-Wno-error=missing-braces" \
  go install -tags sqlite_fts5 github.com/openclaw/wacli/cmd/wacli@latest
```

For local development:

Install Node.js 24 or newer and the pnpm version pinned in `package.json`
(currently 11.25.0). Corepack users can run `corepack pnpm install --frozen-lockfile`
from the checkout to download and verify that pinned version.

```bash
git clone https://github.com/openclaw/wacli.git
cd wacli
make build
make check
./dist/wacli --version
```

The `sqlite_fts5` build tag is required for `messages search` to use the FTS5 index. Without it, search falls back to `LIKE`.

GCC 15 has stricter brace-init warnings; the `-Wno-error=missing-braces` flag keeps the `go-sqlite3` build green there. macOS / clang and older GCC do not need it.

The Makefile is a thin wrapper over the existing pnpm scripts. `make build`
writes `./dist/wacli`; `make check` runs the complete local CI gate.

Repository development, CI, and Docker builds select Go 1.27.1 for its compiler, runtime, cgo, and standard-library fixes. The `toolchain` directive in `go.mod` selects that build version without raising the Go 1.27.0 source minimum; normal Go toolchain auto-selection downloads it when needed.

## Verify the install

```bash
wacli --version
wacli doctor
wacli --help
```

`wacli doctor` checks the store directory, database integrity, FTS5 availability, and (with `--connect`) live connectivity to WhatsApp. See [Doctor](doctor.md).

## Updating

- **Homebrew tap**: `brew upgrade wacli` (or `brew reinstall openclaw/tap/wacli`).
- **GitHub release archives**: download the new tarball / ZIP and replace the binary.
- **Source builds**: `git pull && make build` (or `pnpm build`). Local builds use the version compiled into the source tree; release artifacts inject the tag during GoReleaser builds.

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

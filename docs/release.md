# Release

Read when: cutting a release, debugging release artifacts, or updating the Homebrew tap handoff.

## GitHub Release Artifacts

`wacli` uses GoReleaser (`.goreleaser.yaml` for macOS, `.goreleaser-linux-windows.yaml` for linux/windows) and the GitHub Actions workflow `.github/workflows/release.yml`.

To cut a release:

1. Tag and push:
   - `git tag vX.Y.Z`
   - `git push origin vX.Y.Z`
2. Wait for the GitHub Actions “release” workflow to publish the release artifacts.

To re-release an existing tag, run the workflow manually and pass the tag (e.g. `v0.1.0`).

Expected macOS artifact name (used by the tap updater):

- `wacli-macos-universal.tar.gz`

Other artifacts:

- `wacli-linux-<arch>.tar.gz`
- `wacli-windows-<arch>.zip`

All release builds must use `CGO_ENABLED=1`. `wacli` depends on `go-sqlite3`,
which provides only a runtime stub when cgo is disabled; the CLI build now fails
early if someone tries to compile it with `CGO_ENABLED=0`.

## Homebrew Tap

The release workflow dispatches the `Update Formula` workflow in `steipete/homebrew-tap` after the macOS artifact is published. The tap workflow owns the formula-editing logic and updates both the macOS artifact SHA256 and the Linux source archive SHA256 in `Formula/wacli.rb`.

Required repository secret:

- `HOMEBREW_TAP_TOKEN`: token with permission to run workflows in `steipete/homebrew-tap`

To backfill an existing release, rerun the `release` workflow manually with `tag: vX.Y.Z`.

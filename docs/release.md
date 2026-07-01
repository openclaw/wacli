# Release

Read when: cutting a release, debugging release artifacts, or updating the Homebrew tap handoff.

## GitHub Release Artifacts

`wacli` uses GoReleaser (`.goreleaser.yaml` for macOS, `.goreleaser-linux-windows.yaml` for linux/windows) and the GitHub Actions workflow `.github/workflows/release.yml`.

To cut a release:

1. Replace the target `Unreleased` heading in `CHANGELOG.md` with the release date. The workflow refuses to publish while the matching version section is still `Unreleased`.
2. Tag and push:
   - `git tag vX.Y.Z`
   - `git push origin vX.Y.Z`
3. Wait for the GitHub Actions “release” workflow to publish the release artifacts and use that version's complete `CHANGELOG.md` section as the GitHub Release body.

To re-release an existing tag, run the workflow manually and pass the tag (e.g. `v0.1.0`). The workflow replaces stale release notes with the tagged changelog section.

Expected macOS artifact names (used by the tap updater):

- `wacli_<version>_darwin_amd64.tar.gz`
- `wacli_<version>_darwin_arm64.tar.gz`
- `wacli_<version>_darwin_universal.tar.gz`

Other artifacts:

- `wacli-linux-<arch>.tar.gz`
- `wacli-windows-<arch>.zip`

All release builds must use `CGO_ENABLED=1`. `wacli` depends on `go-sqlite3`,
which provides only a runtime stub when cgo is disabled; the CLI build now fails
early if someone tries to compile it with `CGO_ENABLED=0`.

## Homebrew Tap

The release workflow dispatches the `Update Formula` workflow in `openclaw/homebrew-tap` after all release artifacts are published when the tap token is configured. The tap workflow owns the formula-editing logic and updates the target-specific macOS and Linux binary URLs and SHA256 values in `Formula/wacli.rb`.

Optional repository secret:

- `HOMEBREW_TAP_TOKEN`: token with permission to run workflows in `openclaw/homebrew-tap`

If `HOMEBREW_TAP_TOKEN` is missing, release artifacts are still published and the tap update is skipped with a workflow warning.

To backfill an existing release, rerun the `release` workflow manually with `tag: vX.Y.Z`.

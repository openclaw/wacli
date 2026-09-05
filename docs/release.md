# Release

Read when: preparing or verifying an official wacli release.

wacli uses the fleet-standard reusable Go CLI workflow from `openclaw/release-workflows@v1`. The repository wrapper is intentionally thin: it supplies the wacli artifact contract and maps repository secrets, while the shared workflow owns source freezing, the annotated tag, builds, signing, notarization, independent verification, publication, Homebrew handoff, and the next-version closeout PR.

## Release contract

- Dispatch `.github/workflows/release.yml` from the protected default branch with the version to release.
- `CHANGELOG.md` must contain exactly one dated level-two section for that version, and `cmd/wacli/root.go` must already report it.
- The canonical GoReleaser config builds the Darwin CGO binaries on macOS.
- `.goreleaser-linux-windows.yaml` builds Linux amd64/arm64 and Windows amd64 with the fixed cross-compilers supplied by the shared workflow.
- `LICENSE` and `README.md` are preserved in every archive, and the published checksum asset remains `checksums.txt`.
- Every Darwin binary retains the established `org.openclaw.wacli` identifier and OpenClaw Foundation Developer ID identity.
- The independent rebuild must reproduce every staged Linux and Windows binary byte-for-byte before publication.
- Build and verification use the exact preferred `toolchain` from the frozen commit's `go.mod` (currently Go 1.27.1); the `go` directive remains the Go 1.27.0 source minimum. Historical commits without a `toolchain` directive use their exact `go` version.
- The published release must hand off exact verified assets to `openclaw/homebrew-tap`, then open the next patch's `Unreleased` closeout PR.

## Dispatch

```bash
gh workflow run release.yml --repo openclaw/wacli --ref main -f version=0.15.1
```

Watch the exact run through completion. A successful run is not sufficient on its own: verify the public release is non-draft and non-prerelease, its tag peels to the frozen protected-main commit, every expected asset is present, `checksums.txt` validates the downloaded assets, both native macOS verifier jobs passed, the Homebrew update run succeeded, and the closeout PR was opened.

Retries reuse the immutable annotated version tag and its frozen commit. Never move or replace a consumer release tag to recover from a failed run; fix the shared workflow or caller on `main`, then dispatch the same version again.

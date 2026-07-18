# Release

Read when: preparing official artifacts, verifying a draft, publishing a release, or handing the release to the OpenClaw Homebrew tap.

Official release objects are local, draft-first operations. The signed tag is a separate pre-draft gate because GitHub CLI would otherwise create a lightweight tag. GitHub Actions may build credential-free Linux and Windows inputs and may verify an existing draft, but it cannot sign, notarize, upload, or publish release assets. Run each mutation as a separate maintainer gate.

## Security contract

- Release commit: full SHA already reachable from the protected default branch.
- Workflow trust: both manual workflows require the protected default branch in `github.ref` and the exact workflow path in `github.workflow_ref`; protected tooling is checked out at `github.sha` and asserts that exact HEAD before use.
- Toolchain: exact Go 1.25.12; source and every final thin platform binary pass the reachable-standard-library `govulncheck` gate. Every candidate and universal slice must retain `-trimpath` plus the native symbol table (`-w`, never `-s`) so a trusted local inspector can read the actual linked `main.version` and `main.releaseLinkerSetting` Go strings from ELF, Mach-O, or PE without executing the target. The Darwin builder explicitly disables `LC_DYLD_CHAINED_FIXUPS`; the inspector rejects a chained-fixup Mach-O instead of mistaking its encoded on-disk pointer for an absolute address. Those values must exactly equal `<version>` and `wacli-release-linker-version=[<version>]`; missing, duplicate, malformed, package-scoped, or conflicting linker assignments therefore fail on their resulting runtime value even when the target cannot execute on the verifier host. The supported Homebrew HEAD/source build may continue setting only `main.version`, but that compatibility path cannot satisfy official artifact verification without the exact marker. Host-executable candidates must also report the release version at runtime. Binary verification additionally parses the exact Go build-info header, target `GOOS`/`GOARCH`, clean VCS revision, and reproducible `-trimpath` setting. Any active third-party finding remains visible and unsuppressed.
- macOS identity: exactly one authority, `Developer ID Application: OpenClaw Foundation (FWJYW4S8P8)`; a different Developer ID name with the same Team ID is rejected.
- macOS identifier: `org.openclaw.wacli` on every thin and universal binary.
- macOS signing: trusted timestamp, hardened runtime, and the exact embedded designated requirement `designated => identifier "org.openclaw.wacli" and anchor apple generic and certificate leaf[subject.OU] = "FWJYW4S8P8"` on both final thin binaries, then again on the post-`lipo` universal binary.
- Notarization: `xcrun notarytool` with the runtime `NOTARYTOOL_KEYCHAIN_PROFILE`; the repository stores no notary credentials or profile name. Standalone binaries prove the online ticket with `codesign --verify --strict --check-notarization -R=notarized`.
- Cross-platform provenance: the local downloader authenticates the exact protected workflow path/ID/head SHA, requires that workflow SHA to still equal the protected default-branch head, and binds the run/attempt/event/ref, candidate commit/version inputs, artifact ID/name/GitHub digest, embedded provenance, and per-asset hashes before the local preparer accepts Linux or Windows bytes. Evidence-directory consumption repeats the workflow-SHA/protected-head equality check, so a repacked manifest cannot substitute a stale protected head.
- Draft verification: separate native arm64 and x86_64 jobs from the current protected-default-branch tooling check the exact release ID, tag, commit, changelog-derived title and notes, per-binary VCS revision and clean-build bit, asset inventory, checksums, archive contents, Go version and actual linked runtime-version values, `GOOS`/`GOARCH`, CLI version, architectures, authority, Team ID, identifier, exact embedded designated requirement, online notarization constraint, and native CLI version output. Universal signature metadata is inspected independently with `codesign --arch` for both slices.
- Gatekeeper: standalone CLI assets do not use `spctl --assess`, `syspolicy_check`, or `stapler` as acceptance gates. On macOS 26.5 these tools reject valid notarized standalone code because it is not an app. Gatekeeper proof is a naturally quarantined download executed on a clean VM with no security alert.
- Token boundary: the native verifier job has the scope needed to read a draft, but exposes `github.token` only as `GH_TOKEN` in the exact asset-download step. Checkout never persists credentials. Verification and candidate execution run under `env -i` with no GitHub, Actions, Homebrew, signing, or notarization credential.
- Tag and publication: a separate local gate creates and pushes an annotated signed tag at the exact verified commit. Immediately before its single publish PATCH, publication rereads the draft by numeric ID, requires the identical verified manifest, and requires the current protected default-branch head to still equal the verifier workflow SHA. It then validates the complete response and a fresh numeric-ID GET: published and non-prerelease state, exact ID/tag/target/title/notes/assets, and unchanged manifest. The protected head is reread after publication, and the exact local and remote tag objects plus GitHub's valid signature verification are required before and after the state change. No workflow can publish unsigned Darwin assets.
- Homebrew: the existing OpenClaw tap handoff is bound to the exact public release ID, commit, verified manifest, signed tag, non-prerelease state, inventory, and downloaded checksums. The tap default branch must be protected before dispatch and again after completion. The local closeout authenticates the tap workflow path and ID, binds each formula URL and checksum to its exact macOS/Linux and arm64/amd64 stanza without branch fallthrough, and creates one credential-free environment before the first `brew` command for list, tap, update, formula read, install, prefix lookup, and test. The installed binary is verified before formula test and must byte-match the selected release archive while retaining the exact architecture, Foundation authority/Team/identifier, hardened runtime, timestamp, and online notarization constraint.

The normal `pnpm build`, GoReleaser checks, snapshot builds, and Linux/Windows release builds remain credential-free. Official builders retain Go's top-level `-trimpath`; Go 1.25.12 intentionally omits the informational `-ldflags` buildinfo field in that mode, so verification reads the linked runtime variables themselves. The protected cross-build workflow runs the real GoReleaser release twice with separate fresh HOME, module, build, and temp caches, then requires byte-identical binaries, archives, and checksums before exposing the first output set.

## Expected assets

The draft must contain exactly these seven assets:

- `wacli_<version>_darwin_amd64.tar.gz`
- `wacli_<version>_darwin_arm64.tar.gz`
- `wacli_<version>_darwin_universal.tar.gz`
- `wacli_<version>_linux_amd64.tar.gz`
- `wacli_<version>_linux_arm64.tar.gz`
- `wacli_<version>_windows_amd64.zip`
- `checksums.txt`

Every archive contains only `LICENSE`, `README.md`, and `wacli` (`wacli.exe` on Windows). `checksums.txt` names every archive exactly once.

## Serialized release gates

Set the release coordinates once:

```bash
tag=v0.13.0
version=${tag#v}
commit=$(git rev-parse HEAD)
```

Before any official build, date the matching changelog section, commit it, push protected `main`, and confirm `commit` is the full release SHA.

### 1. Credential-free cross-platform build

Dispatch the workflow from protected `main`, never from the candidate ref:

```bash
gh workflow run release.yml \
  --repo openclaw/wacli \
  --ref main \
  -f commit="$commit" \
  -f version="$version"
```

After it succeeds, record the exact workflow run ID, artifact ID, and protected workflow head SHA. Run the authenticated downloader with `GH_TOKEN` injected only for this process:

```bash
node scripts/download-cross-platform-assets.mjs \
  --run-id "$cross_run_id" \
  --artifact-id "$cross_artifact_id" \
  --workflow-sha "$cross_workflow_sha" \
  --commit "$commit" \
  --version "$version" \
  --output /path/to/authenticated-cross-assets
```

The downloader uses only GitHub `GET` requests, validates the exact workflow/run/artifact provenance and digest, and prints `AUTHENTICATED_CROSS_PLATFORM manifest_sha256=<sha256> ...`. Record that digest as `cross_manifest`, then remove `GH_TOKEN` from the environment before any build, verification, or execution. The artifact contains only credential-free Linux and Windows archives plus provenance metadata; it is not a GitHub Release and cannot publish anything.

### 2. Local Darwin build, signing, and notarization

Run the local preparer through the `release-mac-app` skill's `mac-release codesign-run` wrapper so the dedicated Developer ID keychain is bounded and restored. Set `MAC_RELEASE` to that skill's `scripts/mac-release` helper. Supply `MAC_RELEASE_CODESIGN_IDENTITY` and `NOTARYTOOL_KEYCHAIN_PROFILE` only at runtime through approved credential handling.

```bash
"$MAC_RELEASE" codesign-run -- \
  node scripts/release-local.mjs prepare \
    --tag "$tag" \
    --commit "$commit" \
    --cross-platform-dir /path/to/authenticated-cross-assets \
    --cross-platform-manifest-sha256 "$cross_manifest" \
    --output "dist/release/$tag"
```

Preparation is local and fail-closed. It completes source and every thin platform binary vulnerability check, signs both Darwin thin binaries, creates and signs the universal binary, submits one ZIP containing all three final Darwin binaries to `notarytool`, verifies the online notarization constraint, assembles all seven assets, and re-verifies the complete candidate before moving it into the output directory. It performs no GitHub or Homebrew mutation.

### 3. Create and verify the signed release tag

After local preparation has produced the complete verified candidate, create the annotated signed tag at the exact release commit:

```bash
node scripts/release-local.mjs tag \
  --tag "$tag" \
  --commit "$commit" \
  --candidate-dir "dist/release/$tag" \
  --confirm-signed-tag "$tag"
```

The command re-verifies the candidate without release credentials before any tag mutation, refuses an existing local or remote tag, verifies the local signature and annotation, pushes only the exact tag ref, and confirms the remote annotated object and peeled commit. Record the tag object SHA.

### 4. Create the private draft

The draft command re-verifies the local candidate and the exact signed local, remote, and GitHub tag objects before its first release mutation. It uses `gh release create --verify-tag`, so GitHub cannot infer or create a lightweight tag.

```bash
node scripts/release-local.mjs draft \
  --tag "$tag" \
  --commit "$commit" \
  --candidate-dir "dist/release/$tag"
```

Record the exact numeric draft release ID printed by the command.

### 5. Native protected-branch verification

Dispatch the verifier from protected `main`; selected-ref dispatches are rejected.

```bash
gh workflow run release-verify.yml \
  --repo openclaw/wacli \
  --ref main \
  -f release_id="$release_id" \
  -f tag="$tag" \
  -f commit="$commit"
```

Record the successful verifier run ID and its exact protected workflow head SHA as `verifier_head`. Publication authenticates the numeric workflow ID and exact `.github/workflows/release-verify.yml` path in addition to that SHA; display names and branch names alone are insufficient. The workflow must complete separate arm64 and x86_64 native jobs. Its log must contain both exact architecture markers:

```text
VERIFIED_ARCH arch=arm64 release_id=<id> tag=<tag> commit=<full-sha> manifest_sha256=<sha256>
VERIFIED_ARCH arch=x86_64 release_id=<id> tag=<tag> commit=<full-sha> manifest_sha256=<sha256>
```

### 6. Clean-VM Gatekeeper proof

On a clean macOS 26.5 VM, obtain the exact draft archive through a normal download path that naturally applies quarantine. Do not synthesize quarantine with `xattr`. Execute the host-compatible thin binary and the universal binary, confirm `wacli --version` reports the release version, and confirm macOS presents no Gatekeeper security alert. This is an attribution gate: signing `wacli` still says nothing about the random temporary Swift script used by Contacts import.

Do not use standalone-binary `spctl`, `syspolicy_check`, or `stapler` output as proof. Preserve the VM version, natural-download path, archive checksum, tested binary architecture, command output, and no-alert observation in the private release record.

### 7. Publish the verified draft

Set `release_manifest` to the identical manifest SHA-256 printed by both native verifier jobs. Publication requires the exact successful verifier run, the verifier head still being the current protected default-branch SHA, the signed tag, the clean-VM proof, and explicit confirmations equal to the tag:

```bash
node scripts/release-local.mjs publish \
  --release-id "$release_id" \
  --tag "$tag" \
  --commit "$commit" \
  --verifier-run "$verifier_run" \
  --verifier-head "$verifier_head" \
  --confirm-publish "$tag" \
  --confirm-gatekeeper-vm "$tag"
```

The command authenticates the workflow's numeric ID and exact path, requires separate exact arm64 and x86_64 markers, verifies the signed annotated tag through Git and GitHub, rereads and revalidates the identical draft manifest by numeric ID, and rereads the protected default-branch head immediately before publishing. It validates the complete publish response, repeats the numeric-ID release GET, rereads the protected head after publication, and rechecks the signed tag. If protected `main` advances at either freshness check, the gate fails rather than accepting historical evidence.

### 8. Verify the public release and hand off Homebrew

Use a clean macOS Homebrew host where `brew list --versions wacli` reports no installation. The command downloads the exact release assets by ID, checks GitHub digests and `checksums.txt`, revalidates the signed tag and manifest, requires the tap default branch to be protected before dispatch and after completion, authenticates the exact tap workflow, and checks each target-specific formula URL and SHA-256 inside the correct OS/CPU stanza. Every Homebrew invocation, formula test, and candidate execution shares one credential-free environment created before the initial installed-package check. The installed binary is verified before `brew test`; it must match the selected release archive's binary hash and retain its exact thin architecture, Foundation signing metadata, hardened runtime, timestamp, and online notarization constraint:

```bash
node scripts/release-local.mjs homebrew \
  --release-id "$release_id" \
  --tag "$tag" \
  --commit "$commit" \
  --manifest-sha256 "$release_manifest" \
  --confirm-clean-homebrew-host "$tag"
```

The final `HOMEBREW_VERIFIED` marker records the release, manifest, signed tag object, and exact tap run. Then open the changelog's next patch section as `Unreleased` and commit that closeout separately.

## Contacts permission caveat

`contacts import-system` currently writes its embedded Swift source to a random temporary directory and runs that script with `swift`/`xcrun`. Signing `wacli` does not give that temporary helper a stable code identity and must not be described as stabilizing macOS Contacts permission. A clean-machine VM attribution test remains an explicit release gate; packaging a stable helper is a separate design change.

import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import {
  RELEASE_GO_TOOLCHAIN,
  archiveNames,
  assertCodeSignatureIdentity,
  assertExactInventory,
  assertGoBuildInfo,
  crossPlatformArchiveNames,
  inspectLinkedReleaseVersion,
  parseChecksums,
  releaseArchiveTarget,
  releaseAssetNames,
  releaseManifestDigest,
  runCommand,
  sanitizedExecutionEnv,
  sha256File,
  verifyDarwinSignature,
} from "./release-common.mjs";
import {
  authenticatedCrossPlatformName,
  validateAuthenticatedCrossPlatformDirectory,
  validateCrossPlatformControlPlane,
  validateCrossPlatformProvenance,
} from "./download-cross-platform-assets.mjs";
import {
  crossPlatformProvenanceName,
  writeCrossPlatformProvenance,
} from "./collect-cross-platform-assets.mjs";
import { validateDraftMetadata } from "./download-release-candidate.mjs";
import {
  classifyGovulncheckEvents,
  formatGateResult,
  parseJsonStream,
} from "./govulncheck-stdlib.mjs";
import {
  assertSigningInputs,
  createAndPushSignedTag,
  createDraftRelease,
  dispatchHomebrewHandoff,
  findExactDraftRelease,
  prepareDarwinRelease,
  publishDraftRelease,
  validateGitHubSignedTag,
  validateHomebrewBranch,
  validateHomebrewFormula,
  validateHomebrewRelease,
  validateHomebrewRun,
  validateVerifierJobs,
  validateVerifierRun,
  verifyHomebrewInstalledBinary,
} from "./release-local.mjs";

const releaseBuilds = fs.readFileSync(
  new URL("../.github/workflows/release.yml", import.meta.url),
  "utf8",
);
const releaseVerify = fs.readFileSync(
  new URL("../.github/workflows/release-verify.yml", import.meta.url),
  "utf8",
);
const releaseLocal = fs.readFileSync(new URL("release-local.mjs", import.meta.url), "utf8");
const crossDownload = fs.readFileSync(
  new URL("download-cross-platform-assets.mjs", import.meta.url),
  "utf8",
);
const crossCollector = fs.readFileSync(
  new URL("collect-cross-platform-assets.mjs", import.meta.url),
  "utf8",
);
const crossReleaseConfig = fs.readFileSync(
  new URL("../.goreleaser-linux-windows.yaml", import.meta.url),
  "utf8",
);
const releaseCandidateVerifier = fs.readFileSync(
  new URL("verify-release-candidate.mjs", import.meta.url),
  "utf8",
);

const tag = "v0.12.1";
const version = "0.12.1";
const commit = "a".repeat(40);
const verifierHead = "b".repeat(40);
const expectedBody = "## Changelog\n\n### Security\n\n- Harden release.\n";
const repoRoot = path.dirname(fileURLToPath(new URL("../package.json", import.meta.url)));
const releaseLdflags =
  `-w -X main.version=${version} ` +
  `-X main.releaseLinkerSetting=wacli-release-linker-version=[${version}]`;

function validDisplay(overrides = {}) {
  return [
    `Identifier=${overrides.identifier ?? "org.openclaw.wacli"}`,
    `TeamIdentifier=${overrides.team ?? "FWJYW4S8P8"}`,
    `Authority=${overrides.authority ?? `Developer ID Application: OpenClaw Foundation (${overrides.team ?? "FWJYW4S8P8"})`}`,
    "Authority=Developer ID Certification Authority",
    "CodeDirectory v=20500 size=123 flags=0x10000(runtime) hashes=2+7 location=embedded",
    "Timestamp=9 Jul 2026 at 12:00:00",
  ].join("\n");
}

function validRequirement(team = "FWJYW4S8P8") {
  return (
    'designated => identifier "org.openclaw.wacli" and anchor apple generic and ' +
    `certificate leaf[subject.OU] = "${team}"`
  );
}

function publicationFixture({
  mutateLatestDraft,
  mutatePublished,
  mutateFreshPublished,
  prePublishHead = verifierHead,
  postPublishHead = verifierHead,
} = {}) {
  const assets = releaseAssetNames(version).map((name, index) => ({
    id: index + 1,
    name,
    size: index + 10,
    state: "uploaded",
    digest: `sha256:${String(index + 1).padStart(64, "0")}`,
  }));
  const draft = {
    id: 42,
    tag_name: tag,
    target_commitish: commit,
    name: `wacli ${tag}`,
    body: expectedBody,
    draft: true,
    prerelease: false,
    published_at: null,
    assets,
  };
  const latestDraft = mutateLatestDraft ? mutateLatestDraft(structuredClone(draft)) : draft;
  const publishedBase = {
    ...draft,
    draft: false,
    published_at: "2026-07-09T12:00:00Z",
  };
  const published = mutatePublished ? mutatePublished(structuredClone(publishedBase)) : publishedBase;
  const freshPublished = mutateFreshPublished
    ? mutateFreshPublished(structuredClone(publishedBase))
    : publishedBase;
  const manifestDigest = releaseManifestDigest({ release_id: 42, tag, commit, assets });
  const calls = [];
  let releaseReads = 0;
  let branchReads = 0;
  let patched = false;
  const run = (command, args, options) => {
    calls.push([command, args, options]);
    if (command === "git" && args[0] === "show") {
      return {
        status: 0,
        stdout: "# Changelog\n\n## 0.12.1 - 2026-07-09\n\n### Security\n\n- Harden release.\n",
        stderr: "",
      };
    }
    if (args.includes("/repos/openclaw/wacli/releases/42")) {
      if (args.includes("PATCH")) {
        patched = true;
        return { status: 0, stdout: JSON.stringify(published), stderr: "" };
      }
      releaseReads += 1;
      return {
        status: 0,
        stdout: JSON.stringify(patched ? freshPublished : releaseReads === 1 ? draft : latestDraft),
        stderr: "",
      };
    }
    if (args.includes("/repos/openclaw/wacli/actions/workflows/release-verify.yml")) {
      return {
        status: 0,
        stdout: JSON.stringify({
          id: 7,
          path: ".github/workflows/release-verify.yml",
          state: "active",
        }),
        stderr: "",
      };
    }
    if (args.includes("/repos/openclaw/wacli/branches/main")) {
      branchReads += 1;
      const head =
        branchReads === 1 ? verifierHead : branchReads === 2 ? prePublishHead : postPublishHead;
      return {
        status: 0,
        stdout: JSON.stringify({ name: "main", protected: true, commit: { sha: head } }),
        stderr: "",
      };
    }
    if (args.includes("/repos/openclaw/wacli/actions/runs/99/jobs?filter=latest&per_page=100")) {
      return {
        status: 0,
        stdout: JSON.stringify({
          jobs: [
            {
              id: 100,
              name: "native-darwin-arm64",
              status: "completed",
              conclusion: "success",
              labels: ["macos-15"],
            },
            {
              id: 101,
              name: "native-darwin-x86_64",
              status: "completed",
              conclusion: "success",
              labels: ["macos-15-intel"],
            },
          ],
        }),
        stderr: "",
      };
    }
    if (args.includes("/repos/openclaw/wacli/actions/runs/99")) {
      return {
        status: 0,
        stdout: JSON.stringify({
          id: 99,
          workflow_id: 7,
          path: ".github/workflows/release-verify.yml",
          event: "workflow_dispatch",
          status: "completed",
          conclusion: "success",
          head_branch: "main",
          head_sha: verifierHead,
          head_repository: { full_name: "openclaw/wacli" },
        }),
        stderr: "",
      };
    }
    if (args.includes("/repos/openclaw/wacli")) {
      return {
        status: 0,
        stdout: JSON.stringify({ full_name: "openclaw/wacli", default_branch: "main" }),
        stderr: "",
      };
    }
    if (command === "gh" && args[0] === "run" && args.includes("--log")) {
      const arch = args.includes("100") ? "arm64" : "x86_64";
      return {
        status: 0,
        stdout:
          `VERIFIED_ARCH arch=${arch} release_id=42 tag=${tag} ` +
          `commit=${commit} manifest_sha256=${manifestDigest}\n`,
        stderr: "",
      };
    }
    throw new Error(`unexpected command: ${command} ${args.join(" ")}`);
  };
  return { calls, run };
}

test("credential-free release builds are manual, protected-ref, and non-publishing", () => {
  assert.match(releaseBuilds, /^name: release-builds$/m);
  assert.match(releaseBuilds, /^  workflow_dispatch:$/m);
  assert.doesNotMatch(releaseBuilds, /^  push:$/m);
  assert.match(releaseBuilds, /github\.ref == format\('refs\/heads\/\{0\}'/);
  assert.match(
    releaseBuilds,
    /github\.workflow_ref == format\('\{0\}\/\.github\/workflows\/release\.yml@refs\/heads\/\{1\}'/,
  );
  assert.match(releaseBuilds, /ref: \$\{\{ github\.sha \}\}/);
  assert.match(releaseBuilds, /git -C trusted rev-parse HEAD.*TRUSTED_WORKFLOW_SHA/);
  assert.match(releaseBuilds, /persist-credentials: false/);
  assert.match(releaseBuilds, /env -i[\s\S]*goreleaser release --clean --skip=publish/);
  assert.match(releaseBuilds, /for pass in 1 2; do/);
  assert.match(releaseBuilds, /GOMODCACHE="\$pass_root\/gomodcache"/);
  assert.match(releaseBuilds, /GOCACHE="\$pass_root\/gocache"/);
  assert.match(
    releaseBuilds,
    /cmp "\$RUNNER_TEMP\/cross-repro-1\/dist\/\$file" "\$RUNNER_TEMP\/cross-repro-2\/dist\/\$file"/,
  );
  assert.match(releaseBuilds, /wacli_\$\{RELEASE_VERSION\}_windows_amd64\.zip/);
  assert.match(releaseBuilds, /mv "\$RUNNER_TEMP\/cross-repro-1\/dist" dist/);
  assert.match(releaseBuilds, /REPRODUCIBLE_CROSS_PLATFORM/);
  assert.match(releaseBuilds, /--workflow-ref "\$GITHUB_WORKFLOW_REF"/);
  assert.match(releaseBuilds, /--workflow-sha "\$GITHUB_SHA"/);
  assert.match(releaseBuilds, /--run-id "\$GITHUB_RUN_ID"/);
  assert.doesNotMatch(releaseBuilds, /contents: write|gh release (?:create|upload)|secrets\./);
});

test("native verifier is protected-main and drops tokens before candidate verification", () => {
  assert.match(releaseVerify, /^name: release-verify$/m);
  assert.match(releaseVerify, /^  workflow_dispatch:$/m);
  assert.doesNotMatch(releaseVerify, /^  push:$/m);
  assert.match(releaseVerify, /github\.ref == format\('refs\/heads\/\{0\}'/);
  assert.match(
    releaseVerify,
    /github\.workflow_ref == format\('\{0\}\/\.github\/workflows\/release-verify\.yml@refs\/heads\/\{1\}'/,
  );
  assert.match(releaseVerify, /ref: \$\{\{ github\.sha \}\}/);
  assert.match(releaseVerify, /git rev-parse HEAD.*TRUSTED_WORKFLOW_SHA/);
  assert.doesNotMatch(releaseVerify, /ref: \$\{\{ inputs\./);
  assert.match(releaseVerify, /native-darwin-verifier:[\s\S]*?permissions:\n\s+contents: write/);
  assert.equal((releaseVerify.match(/GH_TOKEN:/g) ?? []).length, 1);
  assert.match(releaseVerify, /arch: arm64[\s\S]*runner: macos-15/);
  assert.match(releaseVerify, /arch: x86_64[\s\S]*runner: macos-15-intel/);
  assert.match(
    releaseVerify,
    /name: Verify candidate with no token or release credential[\s\S]*VERIFY_ARCH: \$\{\{ matrix\.arch \}\}[\s\S]*--host-arch "\$VERIFY_ARCH"/,
  );
  assert.match(
    releaseVerify,
    /name: Verify candidate with no token or release credential[\s\S]*?env -i[\s\S]*?verify-release-candidate\.mjs/,
  );
  assert.doesNotMatch(releaseVerify, /secrets\.|gh release (?:create|upload)/);
  assert.match(
    releaseCandidateVerifier,
    /const hostArch = combinedOutput[\s\S]*const tempDir = fs\.mkdtempSync[\s\S]*return \{[\s\S]*hostArch,/,
  );
  assert.doesNotMatch(releaseCandidateVerifier, /verifyRuntimeVersion:/);
  const finalSignatureIndex = releaseCandidateVerifier.indexOf(
    'verifyDarwinSignature(darwinUniversal, { run, arch: "arm64" })',
  );
  const hostThinRuntimeIndex = releaseCandidateVerifier.indexOf(
    "assertRuntimeVersion(hostThin, version, { run })",
  );
  const universalRuntimeIndex = releaseCandidateVerifier.indexOf(
    "assertRuntimeVersion(darwinUniversal, version, { run })",
  );
  assert.ok(
    finalSignatureIndex >= 0 &&
      finalSignatureIndex < hostThinRuntimeIndex &&
      hostThinRuntimeIndex < universalRuntimeIndex,
  );
});

test("binary build info is bound to the exact clean candidate commit", () => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "wacli-build-info-test-"));
  const binary = path.join(directory, "wacli");
  fs.writeFileSync(binary, "fixture");
  const buildInfo = {
    GoVersion: "go1.26.5",
    Path: "github.com/openclaw/wacli/cmd/wacli",
    Settings: [
      { Key: "-trimpath", Value: "true" },
      { Key: "-tags", Value: "sqlite_fts5" },
      { Key: "CGO_ENABLED", Value: "1" },
      { Key: "GOARCH", Value: "arm64" },
      { Key: "GOOS", Value: "darwin" },
      { Key: "vcs.revision", Value: commit },
      { Key: "vcs.modified", Value: "false" },
    ],
  };
  const runWithInfo = (
    info,
    {
      linked = {
        version,
        releaseLinkerSetting: `wacli-release-linker-version=[${version}]`,
      },
      runtimeOutput = `wacli ${version}\n`,
    } = {},
  ) => (command, args) => {
    if (command === "go" && args[0] === "version") {
      assert.deepEqual(args, ["version", "-m", "-json", binary]);
      return { stdout: JSON.stringify(info), stderr: "" };
    }
    if (command === "go" && args[0] === "run") {
      assert.deepEqual(args, ["run", "./scripts/release-version-inspector", binary]);
      return { stdout: JSON.stringify(linked), stderr: "" };
    }
    assert.equal(command, binary);
    assert.deepEqual(args, ["--version"]);
    return { stdout: runtimeOutput, stderr: "" };
  };
  try {
    assert.doesNotThrow(() =>
      assertGoBuildInfo(binary, version, {
        run: runWithInfo(buildInfo),
        commit,
        expectedGoos: "darwin",
        expectedGoarch: "arm64",
        verifyRuntimeVersion: true,
      }),
    );
    assert.throws(
      () =>
        assertGoBuildInfo(binary, version, {
          run: runWithInfo(buildInfo, { runtimeOutput: "wacli 9.9.9\n" }),
          commit,
          expectedGoos: "darwin",
          expectedGoarch: "arm64",
          verifyRuntimeVersion: true,
        }),
      /--version returned "wacli 9\.9\.9"/,
    );
    assert.throws(
      () =>
        assertGoBuildInfo(binary, version, {
          run: runWithInfo(buildInfo),
          commit: "b".repeat(40),
          expectedGoos: "darwin",
          expectedGoarch: "arm64",
        }),
      /was not built from release commit/,
    );
    assert.throws(
      () =>
        assertGoBuildInfo(binary, version, {
          run: runWithInfo(buildInfo),
          commit,
          expectedGoos: "linux",
          expectedGoarch: "arm64",
        }),
      /target mismatch/,
    );
    assert.throws(
      () =>
        assertGoBuildInfo(binary, version, {
          run: runWithInfo({ ...buildInfo, GoVersion: "go1.26.50" }),
          commit,
          expectedGoos: "darwin",
          expectedGoarch: "arm64",
        }),
      /not go1\.26\.5/,
    );

    const wrongVersionBuildInfo = {
      ...buildInfo,
      Settings: buildInfo.Settings.map((setting) => {
        if (setting.Key === "GOOS") return { ...setting, Value: "windows" };
        if (setting.Key === "GOARCH") return { ...setting, Value: "amd64" };
        return setting;
      }),
    };
    assert.throws(
      () =>
        assertGoBuildInfo(binary, version, {
          run: runWithInfo(wrongVersionBuildInfo, {
            linked: {
              version: "9.9.9",
              releaseLinkerSetting: `wacli-release-linker-version=[${version}]`,
            },
          }),
          commit,
          expectedGoos: "windows",
          expectedGoarch: "amd64",
        }),
      /linked runtime version "9\.9\.9"/,
    );
    assert.throws(
      () =>
        assertGoBuildInfo(binary, version, {
          run: runWithInfo(buildInfo, {
            linked: {
              version,
              releaseLinkerSetting: "wacli-release-linker-version=[9.9.9]",
            },
          }),
          commit,
          expectedGoos: "darwin",
          expectedGoarch: "arm64",
        }),
      /linked release marker/,
    );
    assert.throws(
      () =>
        assertGoBuildInfo(binary, version, {
          run: runWithInfo({
            ...buildInfo,
            Settings: buildInfo.Settings.filter((setting) => setting.Key !== "-trimpath"),
          }),
          commit,
          expectedGoos: "darwin",
          expectedGoarch: "arm64",
        }),
      /missing the reproducible -trimpath/,
    );
  } finally {
    fs.rmSync(directory, { recursive: true, force: true });
  }
});

test("official builders preserve trimpath and linked-version symbols", () => {
  const darwinBuilder = releaseLocal.slice(
    releaseLocal.indexOf("function buildDarwinBinary"),
    releaseLocal.indexOf("function signBinary"),
  );
  assert.match(darwinBuilder, /"-trimpath"/);
  assert.equal((crossReleaseConfig.match(/^\s+- -trimpath$/gm) ?? []).length, 3);
  assert.equal(
    (crossReleaseConfig.match(/^\s+mod_timestamp: "\{\{ \.CommitTimestamp \}\}"$/gm) ?? [])
      .length,
    3,
  );
  assert.match(darwinBuilder, /-ldflags/);
  assert.match(darwinBuilder, /-Wl,-no_fixup_chains/);
  assert.doesNotMatch(darwinBuilder, /-s -w/);
  assert.doesNotMatch(crossReleaseConfig, /-s -w/);
  assert.match(darwinBuilder, /`-w -X main\.version=/);
  assert.equal((crossReleaseConfig.match(/^\s+ldflags:$/gm) ?? []).length, 3);
});

test("real ELF, Mach-O, and PE binaries expose exact linked release versions under trimpath", () => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "wacli-real-build-info-test-"));
  const buildEnv = sanitizedExecutionEnv({ CGO_ENABLED: "0", GOTOOLCHAIN: RELEASE_GO_TOOLCHAIN });
  const build = (name, ldflags, target = {}) => {
    const binary = path.join(directory, name);
    const targetEnv = { ...buildEnv };
    if (target.goos) targetEnv.GOOS = target.goos;
    if (target.goarch) targetEnv.GOARCH = target.goarch;
    runCommand(
      "go",
      [
        "build",
        "-trimpath",
        "-ldflags",
        ldflags,
        "-o",
        binary,
        "./scripts/testdata/release-buildinfo",
      ],
      { cwd: repoRoot, env: targetEnv },
    );
    return binary;
  };

  try {
    for (const target of [
      { goos: "darwin", goarch: "arm64", name: "darwin-arm64" },
      { goos: "linux", goarch: "amd64", name: "linux-amd64" },
      { goos: "linux", goarch: "arm64", name: "linux-arm64" },
      { goos: "windows", goarch: "amd64", name: "windows-amd64.exe" },
    ]) {
      const binary = build(target.name, releaseLdflags, target);
      assert.deepEqual(inspectLinkedReleaseVersion(binary), {
        version,
        releaseLinkerSetting: `wacli-release-linker-version=[${version}]`,
      });
      const info = JSON.parse(
        runCommand("go", ["version", "-m", "-json", binary], { env: buildEnv }).stdout,
      );
      const settings = new Map(info.Settings.map((setting) => [setting.Key, setting.Value]));
      assert.equal(settings.get("-trimpath"), "true");
      assert.equal(settings.has("-ldflags"), false);
    }

    const wrongLdflags = releaseLdflags.replace("main.version=0.12.1", "main.version=9.9.9");
    assert.equal(inspectLinkedReleaseVersion(build("wrong-version", wrongLdflags)).version, "9.9.9");

    const packageScoped =
      `-w -X github.com/openclaw/wacli/scripts/testdata/release-buildinfo.version=${version} ` +
      `-X main.releaseLinkerSetting=wacli-release-linker-version=[${version}]`;
    assert.throws(
      () => inspectLinkedReleaseVersion(build("package-scoped", packageScoped)),
      /missing symbol main\.version\.str/,
    );
  } finally {
    fs.rmSync(directory, { recursive: true, force: true });
  }
});

test(
  "actual CGO Darwin release linking disables chained fixups before inspection",
  { skip: process.platform !== "darwin" },
  () => {
    const directory = fs.mkdtempSync(path.join(os.tmpdir(), "wacli-cgo-build-info-test-"));
    const build = (name, cgoLdflags) => {
      const binary = path.join(directory, name);
      runCommand(
        "go",
        [
          "build",
          "-trimpath",
          "-tags",
          "sqlite_fts5",
          "-ldflags",
          releaseLdflags,
          "-o",
          binary,
          "./cmd/wacli",
        ],
        {
          cwd: repoRoot,
          env: sanitizedExecutionEnv({
            CGO_CFLAGS: "-Wno-error=missing-braces",
            CGO_ENABLED: "1",
            CGO_LDFLAGS: cgoLdflags,
            GOTOOLCHAIN: RELEASE_GO_TOOLCHAIN,
          }),
        },
      );
      return binary;
    };

    try {
      const chained = build("chained", "");
      const chainedLoads = runCommand("otool", ["-l", chained]).stdout;
      if (chainedLoads.includes("LC_DYLD_CHAINED_FIXUPS")) {
        assert.throws(
          () => inspectLinkedReleaseVersion(chained),
          /Mach-O chained fixups are unsupported/,
        );
      }

      const inspectable = build("inspectable", "-Wl,-no_fixup_chains");
      assert.doesNotMatch(runCommand("otool", ["-l", inspectable]).stdout, /LC_DYLD_CHAINED_FIXUPS/);
      assert.deepEqual(inspectLinkedReleaseVersion(inspectable), {
        version,
        releaseLinkerSetting: `wacli-release-linker-version=[${version}]`,
      });
    } finally {
      fs.rmSync(directory, { recursive: true, force: true });
    }
  },
);

test("missing notary profile fails before any release command", () => {
  const calls = [];
  assert.throws(
    () =>
      prepareDarwinRelease({
        tag,
        commit,
        crossPlatformDir: "/unused",
        outputDir: "/unused",
        platform: "darwin",
        env: { MAC_RELEASE_CODESIGN_IDENTITY: "identity" },
        run: (...args) => calls.push(args),
      }),
    /NOTARYTOOL_KEYCHAIN_PROFILE/,
  );
  assert.deepEqual(calls, []);
  assert.throws(() => assertSigningInputs({}), /NOTARYTOOL_KEYCHAIN_PROFILE/);
});

test("official preparation rejects release tokens before any command", () => {
  const calls = [];
  assert.throws(
    () =>
      prepareDarwinRelease({
        tag,
        commit,
        crossPlatformDir: "/unused",
        crossPlatformManifest: "a".repeat(64),
        outputDir: "/unused",
        platform: "darwin",
        env: {
          GH_TOKEN: "redacted",
          MAC_RELEASE_CODESIGN_IDENTITY: "identity",
          NOTARYTOOL_KEYCHAIN_PROFILE: "profile",
        },
        run: (...args) => calls.push(args),
      }),
    /credential-bearing environment: GH_TOKEN/,
  );
  assert.deepEqual(calls, []);
});

test("wrong signing identity is rejected", () => {
  assert.throws(
    () => assertCodeSignatureIdentity(validDisplay({ team: "WRONGTEAM1" }), validRequirement("WRONGTEAM1")),
    /wrong signing team/,
  );
  assert.throws(
    () =>
      assertCodeSignatureIdentity(
        validDisplay({ authority: "Developer ID Application: Impostor (FWJYW4S8P8)" }),
        validRequirement(),
      ),
    /authority is not exactly Developer ID Application: OpenClaw Foundation \(FWJYW4S8P8\)/,
  );
});

test("embedded designated requirement must match exactly", () => {
  assert.throws(
    () =>
      assertCodeSignatureIdentity(
        validDisplay(),
        `${validRequirement()} and certificate leaf[subject.CN] = "unexpected"`,
      ),
    /embedded designated requirement mismatch/,
  );
});

test("standalone CLI requires online notarization without raw spctl assessment", () => {
  const calls = [];
  const run = (command, args) => {
    calls.push([command, args]);
    if (["spctl", "syspolicy_check", "stapler"].includes(command)) {
      throw new Error(`standalone CLI verifier must not call ${command}`);
    }
    if (command === "codesign" && args.includes("--display") && args.includes("--requirements")) {
      return { stdout: validRequirement(), stderr: "" };
    }
    if (command === "codesign" && args.includes("--display")) {
      return { stdout: "", stderr: validDisplay() };
    }
    return { stdout: "", stderr: "" };
  };
  assert.doesNotThrow(() => verifyDarwinSignature("/tmp/wacli", { run }));
  assert.ok(
    calls.some(
      ([command, args]) =>
        command === "codesign" &&
        args.join(" ") === "--verify --strict --check-notarization -R=notarized /tmp/wacli",
    ),
  );
  assert.ok(!calls.some(([command]) => ["spctl", "syspolicy_check", "stapler"].includes(command)));
});

test("failed online notarization constraint is rejected", () => {
  const run = (command, args) => {
    if (command === "codesign" && args.includes("--display") && args.includes("--requirements")) {
      return { stdout: validRequirement(), stderr: "" };
    }
    if (command === "codesign" && args.includes("--display")) {
      return { stdout: "", stderr: validDisplay() };
    }
    if (command === "codesign" && args.includes("-R=notarized")) {
      throw new Error("online notarization constraint failed");
    }
    return { stdout: "", stderr: "" };
  };
  assert.throws(() => verifyDarwinSignature("/tmp/wacli", { run }), /constraint failed/);
});

test("universal signature metadata is inspected independently for both slices", () => {
  const displayArches = [];
  const run = (command, args) => {
    if (command === "codesign" && args.includes("--display") && args.includes("--requirements")) {
      return { stdout: validRequirement(), stderr: "" };
    }
    if (command === "codesign" && args.includes("--display")) {
      displayArches.push(args[args.indexOf("--arch") + 1]);
      return { stdout: "", stderr: validDisplay() };
    }
    return { stdout: "", stderr: "" };
  };
  verifyDarwinSignature("/tmp/wacli-universal", { run, arch: "x86_64" });
  verifyDarwinSignature("/tmp/wacli-universal", { run, arch: "arm64" });
  assert.deepEqual(displayArches.sort(), ["arm64", "x86_64"]);
});

test("malformed release and checksum inventories fail closed", () => {
  assert.throws(
    () => assertExactInventory(["checksums.txt", "extra"], releaseAssetNames(version), "asset"),
    /inventory mismatch/,
  );
  const badChecksums = archiveNames(version)
    .map((name, index) => `${"a".repeat(64)}  ${index === 0 ? "../escape" : name}`)
    .join("\n");
  assert.throws(() => parseChecksums(badChecksums, archiveNames(version)), /malformed checksums/);
});

test("every release archive name has an exact GOOS and GOARCH contract", () => {
  assert.deepEqual(
    archiveNames(version).map((name) => [name, releaseArchiveTarget(name, version)]),
    [
      [`wacli_${version}_darwin_amd64.tar.gz`, { goos: "darwin", goarch: "amd64" }],
      [`wacli_${version}_darwin_arm64.tar.gz`, { goos: "darwin", goarch: "arm64" }],
      [`wacli_${version}_darwin_universal.tar.gz`, { goos: "darwin", goarch: "universal" }],
      [`wacli_${version}_linux_amd64.tar.gz`, { goos: "linux", goarch: "amd64" }],
      [`wacli_${version}_linux_arm64.tar.gz`, { goos: "linux", goarch: "arm64" }],
      [`wacli_${version}_windows_amd64.zip`, { goos: "windows", goarch: "amd64" }],
    ],
  );
  assert.match(releaseCandidateVerifier, /const target = releaseArchiveTarget\(archiveName, version\)/);
  assert.match(crossCollector, /const target = releaseArchiveTarget\(name, version\)/);
});

test("stdlib gate uses reachable findings and keeps third-party findings visible", () => {
  const events = parseJsonStream(
    [
      { osv: { id: "GO-2026-5856", summary: "stdlib advisory without a reachable finding" } },
      { osv: { id: "GO-THIRD-PARTY", summary: "active dependency advisory" } },
      { finding: { osv: "GO-THIRD-PARTY", trace: [{ module: "example.com/dependency" }] } },
    ]
      .map((event) => JSON.stringify(event, null, 2))
      .join("\n"),
  );
  const result = classifyGovulncheckEvents(events);
  assert.deepEqual(result.stdlib, []);
  assert.deepEqual(result.thirdParty.map((finding) => finding.id), ["GO-THIRD-PARTY"]);
  assert.match(formatGateResult(result), /remain reported and unsuppressed: GO-THIRD-PARTY/);
  assert.match(formatGateResult(result), /no reachable standard-library vulnerabilities/);

  const reachableStdlib = classifyGovulncheckEvents([
    { osv: { id: "GO-STDLIB", summary: "reachable stdlib advisory" } },
    {
      finding: {
        osv: "GO-STDLIB",
        trace: [{ module: "stdlib", version: "go1.25.11", package: "crypto/tls", function: "Conn.Handshake" }],
      },
    },
  ]);
  assert.deepEqual(reachableStdlib.stdlib.map((finding) => finding.id), ["GO-STDLIB"]);
});

test("cross-platform artifact control-plane coordinates fail closed", () => {
  const repository = { id: 1, full_name: "openclaw/wacli", default_branch: "main" };
  const protectedBranch = {
    name: "main",
    protected: true,
    commit: { sha: verifierHead },
  };
  const workflow = { id: 12, path: ".github/workflows/release.yml", state: "active" };
  const workflowRun = {
    id: 123,
    workflow_id: 12,
    path: ".github/workflows/release.yml",
    event: "workflow_dispatch",
    display_title: `release-builds commit=${commit} version=${version}`,
    status: "completed",
    conclusion: "success",
    head_branch: "main",
    head_sha: verifierHead,
    head_repository: { full_name: "openclaw/wacli" },
  };
  const artifact = {
    id: 456,
    name: `wacli-${version}-cross-${commit}`,
    expired: false,
    size_in_bytes: 123,
    digest: `sha256:${"d".repeat(64)}`,
    workflow_run: { id: 123, head_sha: verifierHead },
  };
  const options = {
    repository,
    protectedBranch,
    workflow,
    workflowRun,
    artifact,
    runId: 123,
    artifactId: 456,
    workflowSha: verifierHead,
    commit,
    version,
  };
  assert.doesNotThrow(() => validateCrossPlatformControlPlane(options));
  assert.throws(
    () =>
      validateCrossPlatformControlPlane({
        ...options,
        workflowRun: { ...workflowRun, path: ".github/workflows/untrusted.yml" },
      }),
    /workflow run provenance mismatch/,
  );
  assert.throws(
    () =>
      validateCrossPlatformControlPlane({
        ...options,
        workflowRun: { ...workflowRun, display_title: "release-builds commit=wrong version=9.9.9" },
      }),
    /workflow run provenance mismatch/,
  );
  assert.throws(
    () =>
      validateCrossPlatformControlPlane({
        ...options,
        artifact: { ...artifact, name: "wrong" },
      }),
    /artifact identity mismatch/,
  );
  assert.throws(
    () =>
      validateCrossPlatformControlPlane({
        ...options,
        protectedBranch: { ...protectedBranch, protected: false },
      }),
    /not a protected default branch/,
  );
  assert.throws(
    () =>
      validateCrossPlatformControlPlane({
        ...options,
        protectedBranch: {
          ...protectedBranch,
          commit: { sha: "c".repeat(40) },
        },
      }),
    /stale relative to protected default-branch head/,
  );
  assert.throws(
    () =>
      validateCrossPlatformControlPlane({
        ...options,
        artifact: { ...artifact, digest: null },
      }),
    /exact SHA-256 digest/,
  );
});

test("cross-platform provenance binds dispatch inputs and asset digests", () => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "wacli-cross-provenance-test-"));
  try {
    for (const name of crossPlatformArchiveNames(version)) {
      fs.writeFileSync(path.join(directory, name), name);
    }
    const provenance = writeCrossPlatformProvenance({
      outputDir: directory,
      version,
      commit,
      repository: "openclaw/wacli",
      workflowPath: ".github/workflows/release.yml",
      workflowRef: "openclaw/wacli/.github/workflows/release.yml@refs/heads/main",
      workflowSha: verifierHead,
      runId: 123,
      runAttempt: 2,
      event: "workflow_dispatch",
      ref: "refs/heads/main",
    });
    assert.doesNotThrow(() =>
      validateCrossPlatformProvenance(provenance, {
        sourceDir: directory,
        version,
        commit,
        workflowSha: verifierHead,
        runId: 123,
        runAttempt: 2,
        defaultBranch: "main",
      }),
    );
    const provenanceFile = path.join(directory, crossPlatformProvenanceName);
    const authenticatedFile = path.join(directory, authenticatedCrossPlatformName);
    const authenticated = {
      schema: 1,
      repository: "openclaw/wacli",
      default_branch: "main",
      protected_ref: true,
      protected_branch_head: verifierHead,
      workflow_id: 77,
      workflow_path: ".github/workflows/release.yml",
      workflow_sha: verifierHead,
      run_id: 123,
      run_attempt: 2,
      artifact_id: 456,
      artifact_name: `wacli-${version}-cross-${commit}`,
      artifact_digest: `sha256:${"d".repeat(64)}`,
      provenance_sha256: sha256File(provenanceFile),
      inputs: { commit, version },
    };
    fs.writeFileSync(authenticatedFile, `${JSON.stringify(authenticated, null, 2)}\n`);
    assert.doesNotThrow(() =>
      validateAuthenticatedCrossPlatformDirectory({
        sourceDir: directory,
        version,
        commit,
        manifestDigest: sha256File(authenticatedFile),
      }),
    );

    fs.writeFileSync(
      authenticatedFile,
      `${JSON.stringify({ ...authenticated, protected_branch_head: "c".repeat(40) }, null, 2)}\n`,
    );
    assert.throws(
      () =>
        validateAuthenticatedCrossPlatformDirectory({
          sourceDir: directory,
          version,
          commit,
          manifestDigest: sha256File(authenticatedFile),
        }),
      /workflow SHA is not the protected branch head/,
    );
    assert.throws(
      () =>
        validateCrossPlatformProvenance(
          { ...provenance, inputs: { ...provenance.inputs, version: "9.9.9" } },
          {
            sourceDir: directory,
            version,
            commit,
            workflowSha: verifierHead,
            runId: 123,
            runAttempt: 2,
            defaultBranch: "main",
          },
        ),
      /provenance manifest mismatch/,
    );
  } finally {
    fs.rmSync(directory, { recursive: true, force: true });
  }
  assert.match(releaseLocal, /cross-platform-manifest-sha256/);
  assert.doesNotMatch(crossDownload, /"--method",\s*"(?:POST|PATCH|DELETE)"/);
});

test("exact draft metadata rejects extra or partially uploaded assets", () => {
  const assets = releaseAssetNames(version).map((name, index) => ({
    id: index + 1,
    name,
    size: 1,
    state: "uploaded",
  }));
  const metadata = {
    id: 42,
    tag_name: tag,
    target_commitish: commit,
    name: `wacli ${tag}`,
    body: expectedBody,
    draft: true,
    prerelease: false,
    published_at: null,
    assets,
  };
  assert.doesNotThrow(() =>
    validateDraftMetadata(metadata, { releaseId: 42, tag, commit, version, expectedBody }),
  );
  assert.throws(
    () =>
      validateDraftMetadata(
        { ...metadata, assets: [...assets, { id: 99, name: "extra", size: 1, state: "uploaded" }] },
        { releaseId: 42, tag, commit, version, expectedBody },
      ),
    /inventory mismatch/,
  );
  assert.throws(
    () =>
      validateDraftMetadata(
        { ...metadata, assets: assets.map((asset, index) => (index ? asset : { ...asset, state: "new" })) },
        { releaseId: 42, tag, commit, version, expectedBody },
      ),
    /not fully uploaded/,
  );
  assert.throws(
    () =>
      validateDraftMetadata(
        { ...metadata, body: "stale notes" },
        { releaseId: 42, tag, commit, version, expectedBody },
      ),
    /release notes do not match/,
  );
});

test("failed local verification cannot create even a draft", () => {
  const calls = [];
  assert.throws(
    () =>
      createDraftRelease({
        tag,
        commit,
        candidateDir: "/unused",
        verify: () => {
          throw new Error("ticket failed");
        },
        run: (...args) => calls.push(args),
      }),
    /ticket failed/,
  );
  assert.deepEqual(calls, []);
});

test("signed tag creation cannot start before local candidate verification", () => {
  const calls = [];
  assert.throws(
    () =>
      createAndPushSignedTag({
        tag,
        commit,
        candidateDir: "/unused",
        confirm: tag,
        verify: () => {
          throw new Error("candidate failed");
        },
        run: (...args) => calls.push(args),
      }),
    /candidate failed/,
  );
  assert.deepEqual(calls, []);
});

test("draft creation requires the exact signed remote tag", () => {
  const calls = [];
  assert.throws(
    () =>
      createDraftRelease({
        tag,
        commit,
        candidateDir: "/unused",
        verify: () => {},
        verifyTag: () => {
          throw new Error("signed tag missing");
        },
        run: (...args) => calls.push(args),
      }),
    /signed tag missing/,
  );
  assert.deepEqual(calls, []);
  assert.match(releaseLocal, /"--draft",\s*"--verify-tag"/);
});

test("failed draft upload rolls back only the exact partial draft", () => {
  const calls = [];
  let releaseEnumerations = 0;
  const run = (command, args) => {
    calls.push([command, args]);
    if (command === "git" && args.includes("ls-remote")) return { status: 0, stdout: "", stderr: "" };
    if (command === "git" && args.includes("show")) {
      return {
        status: 0,
        stdout: "# Changelog\n\n## 0.12.1 - 2026-07-09\n\n### Security\n\n- Harden release.\n",
        stderr: "",
      };
    }
    if (command === "gh" && args[0] === "release") throw new Error("upload failed");
    if (
      command === "gh" &&
      args.some((arg) => arg.startsWith("/repos/openclaw/wacli/releases?per_page=100&page="))
    ) {
      releaseEnumerations += 1;
      if (releaseEnumerations === 1) return { status: 0, stdout: "[]", stderr: "" };
      return {
        status: 0,
        stdout: JSON.stringify([
          {
            id: 42,
            draft: true,
            tag_name: tag,
            target_commitish: commit,
          },
        ]),
        stderr: "",
      };
    }
    return { status: 0, stdout: "", stderr: "" };
  };

  assert.throws(
    () =>
      createDraftRelease({
        tag,
        commit,
        candidateDir: "/unused",
        verify: () => {},
        verifyTag: () => {},
        run,
      }),
    /upload failed/,
  );
  assert.ok(
    calls.some(
      ([command, args]) =>
        command === "gh" &&
        args.join(" ") === "api --method DELETE /repos/openclaw/wacli/releases/42",
    ),
  );
  assert.ok(!calls.some(([, args]) => args.includes("PATCH")));
});

test("draft enumeration rejects ambiguous authenticated matches", () => {
  const run = () => ({
    status: 0,
    stdout: JSON.stringify([
      { id: 41, tag_name: tag, draft: true },
      { id: 42, tag_name: tag, draft: true },
    ]),
    stderr: "",
  });
  assert.throws(() => findExactDraftRelease(tag, run), /exactly one authenticated draft/);
  assert.doesNotMatch(releaseLocal.slice(0, releaseLocal.indexOf("export function dispatchHomebrewHandoff")),
    /releases\/tags\/\$\{options\.tag\}/,
  );
});

test("publication accepts GitHub's embedded-signature tag message and rejects unsigned tags", () => {
  const tagObjectSha = "d".repeat(40);
  const tagRef = {
    ref: `refs/tags/${tag}`,
    object: { type: "tag", sha: tagObjectSha },
  };
  const signature = "-----BEGIN SSH SIGNATURE-----\nsigned\n-----END SSH SIGNATURE-----";
  const tagObject = {
    sha: tagObjectSha,
    tag,
    message: `wacli ${version}\n${signature}\n`,
    object: { type: "commit", sha: commit },
    verification: { verified: true, reason: "valid", signature },
  };
  assert.doesNotThrow(() =>
    validateGitHubSignedTag({ tag, commit, tagObjectSha, tagRef, tagObject }),
  );
  assert.throws(
    () =>
      validateGitHubSignedTag({
        tag,
        commit,
        tagObjectSha,
        tagRef,
        tagObject: { ...tagObject, message: `wacli ${version}` },
      }),
    /signed annotated release tag/,
  );
  assert.throws(
    () =>
      validateGitHubSignedTag({
        tag,
        commit,
        tagObjectSha,
        tagRef: { ...tagRef, object: { type: "commit", sha: commit } },
        tagObject,
      }),
    /signed annotated release tag/,
  );
  assert.throws(
    () =>
      validateGitHubSignedTag({
        tag,
        commit,
        tagObjectSha,
        tagRef,
        tagObject: { ...tagObject, verification: { verified: false, reason: "unsigned" } },
      }),
    /signed annotated release tag/,
  );
  assert.match(releaseLocal, /\["tag", "--sign", "--annotate", "--message"/);
  const publishSource = releaseLocal.slice(
    releaseLocal.indexOf("export function publishDraftRelease"),
    releaseLocal.indexOf("export function validateHomebrewRelease"),
  );
  assert.ok(
    publishSource.indexOf("verifyGitHubSignedReleaseTag") < publishSource.indexOf('"PATCH"'),
    "signed tag verification must precede publication",
  );
});

test("stale verifier evidence cannot publish a draft", () => {
  const assets = releaseAssetNames(version).map((name, index) => ({
    id: index + 1,
    name,
    size: 1,
    state: "uploaded",
  }));
  const calls = [];
  const run = (command, args) => {
    calls.push([command, args]);
    if (command === "git" && args.includes("show")) {
      return {
        status: 0,
        stdout: "# Changelog\n\n## 0.12.1 - 2026-07-09\n\n### Security\n\n- Harden release.\n",
        stderr: "",
      };
    }
    if (args.includes(`/repos/openclaw/wacli/releases/42`)) {
      return {
        status: 0,
        stdout: JSON.stringify({
          id: 42,
          tag_name: tag,
          target_commitish: commit,
          name: `wacli ${tag}`,
          body: expectedBody,
          draft: true,
          prerelease: false,
          published_at: null,
          assets,
        }),
        stderr: "",
      };
    }
    if (args.includes("/repos/openclaw/wacli")) {
      return {
        status: 0,
        stdout: JSON.stringify({ full_name: "openclaw/wacli", default_branch: "main" }),
        stderr: "",
      };
    }
    if (args.includes("/repos/openclaw/wacli/actions/workflows/release-verify.yml")) {
      return {
        status: 0,
        stdout: JSON.stringify({
          id: 7,
          path: ".github/workflows/release-verify.yml",
          state: "active",
        }),
        stderr: "",
      };
    }
    if (args.includes("/repos/openclaw/wacli/branches/main")) {
      return {
        status: 0,
        stdout: JSON.stringify({ name: "main", protected: true, commit: { sha: verifierHead } }),
        stderr: "",
      };
    }
    if (args.includes("/repos/openclaw/wacli/actions/runs/99")) {
      return {
        status: 0,
        stdout: JSON.stringify({
          id: 99,
          workflow_id: 7,
          path: ".github/workflows/release-verify.yml",
          event: "workflow_dispatch",
          status: "completed",
          conclusion: "success",
          head_branch: "main",
          head_sha: verifierHead,
          head_repository: { full_name: "openclaw/wacli" },
        }),
        stderr: "",
      };
    }
    if (args.includes("/repos/openclaw/wacli/actions/runs/99/jobs?filter=latest&per_page=100")) {
      return {
        status: 0,
        stdout: JSON.stringify({
          jobs: [
            {
              id: 100,
              name: "native-darwin-arm64",
              status: "completed",
              conclusion: "success",
              labels: ["macos-15"],
            },
            {
              id: 101,
              name: "native-darwin-x86_64",
              status: "completed",
              conclusion: "success",
              labels: ["macos-15-intel"],
            },
          ],
        }),
        stderr: "",
      };
    }
    if (command === "gh" && args[0] === "run" && args.includes("--log")) {
      return { status: 0, stdout: "VERIFIED stale candidate", stderr: "" };
    }
    throw new Error(`unexpected command: ${command} ${args.join(" ")}`);
  };

  assert.throws(
    () =>
      publishDraftRelease({
        releaseId: 42,
        tag,
        commit,
        verifierRun: 99,
        verifierHead,
        confirm: tag,
        vmConfirm: tag,
        run,
      }),
    /exact arm64 candidate marker/,
  );
  assert.ok(!calls.some(([, args]) => args.includes("PATCH")));
});

test("publication rereads the identical draft and validates the complete PATCH response", () => {
  const changedDraft = publicationFixture({
    mutateLatestDraft: (draft) => {
      draft.assets[0].size += 1;
      return draft;
    },
  });
  assert.throws(
    () =>
      publishDraftRelease({
        releaseId: 42,
        tag,
        commit,
        verifierRun: 99,
        verifierHead,
        confirm: tag,
        vmConfirm: tag,
        verifyTag: () => "d".repeat(40),
        run: changedDraft.run,
      }),
    /draft release manifest changed after native verification/,
  );
  assert.ok(!changedDraft.calls.some(([, args]) => args.includes("PATCH")));

  const invalidPublishedResponses = [
    [(release) => ({ ...release, draft: true }), /published, non-prerelease/],
    [(release) => ({ ...release, prerelease: true }), /published, non-prerelease/],
    [(release) => ({ ...release, tag_name: "v9.9.9" }), /published release tag mismatch/],
    [(release) => ({ ...release, target_commitish: "c".repeat(40) }), /target commit mismatch/],
    [(release) => ({ ...release, name: "wrong title" }), /title mismatch/],
    [(release) => ({ ...release, body: "wrong notes" }), /notes do not match/],
    [
      (release) => ({
        ...release,
        assets: release.assets.map((asset, index) =>
          index === 0 ? { ...asset, size: asset.size + 1 } : asset,
        ),
      }),
      /published release manifest differs from the verified draft manifest/,
    ],
  ];
  for (const [mutatePublished, expectedError] of invalidPublishedResponses) {
    const fixture = publicationFixture({ mutatePublished });
    assert.throws(
      () =>
        publishDraftRelease({
          releaseId: 42,
          tag,
          commit,
          verifierRun: 99,
          verifierHead,
          confirm: tag,
          vmConfirm: tag,
          verifyTag: () => "d".repeat(40),
          run: fixture.run,
        }),
      expectedError,
    );
    assert.equal(fixture.calls.filter(([, args]) => args.includes("PATCH")).length, 1);
  }

  const changedFreshRead = publicationFixture({
    mutateFreshPublished: (release) => ({ ...release, name: "wrong fresh title" }),
  });
  assert.throws(
    () =>
      publishDraftRelease({
        releaseId: 42,
        tag,
        commit,
        verifierRun: 99,
        verifierHead,
        confirm: tag,
        vmConfirm: tag,
        verifyTag: () => "d".repeat(40),
        run: changedFreshRead.run,
      }),
    /published release title mismatch/,
  );
  assert.equal(changedFreshRead.calls.filter(([, args]) => args.includes("PATCH")).length, 1);
  assert.equal(
    changedFreshRead.calls.filter(
      ([, args]) => args.includes("/repos/openclaw/wacli/releases/42") && !args.includes("PATCH"),
    ).length,
    3,
  );

  const stalePrePublishHead = publicationFixture({ prePublishHead: "c".repeat(40) });
  assert.throws(
    () =>
      publishDraftRelease({
        releaseId: 42,
        tag,
        commit,
        verifierRun: 99,
        verifierHead,
        confirm: tag,
        vmConfirm: tag,
        verifyTag: () => "d".repeat(40),
        run: stalePrePublishHead.run,
      }),
    /pre-publication head is not the current protected default-branch commit/,
  );
  assert.ok(!stalePrePublishHead.calls.some(([, args]) => args.includes("PATCH")));

  const stalePostPublishHead = publicationFixture({ postPublishHead: "c".repeat(40) });
  assert.throws(
    () =>
      publishDraftRelease({
        releaseId: 42,
        tag,
        commit,
        verifierRun: 99,
        verifierHead,
        confirm: tag,
        vmConfirm: tag,
        verifyTag: () => "d".repeat(40),
        run: stalePostPublishHead.run,
      }),
    /post-publication head is not the current protected default-branch commit/,
  );
  assert.equal(stalePostPublishHead.calls.filter(([, args]) => args.includes("PATCH")).length, 1);

  const publishSource = releaseLocal.slice(
    releaseLocal.indexOf("export function publishDraftRelease"),
    releaseLocal.indexOf("export function validateHomebrewRelease"),
  );
  const rereadIndex = publishSource.indexOf("const latestDraft = readReleaseById");
  const prePublishHeadIndex = publishSource.indexOf("const prePublishRepository");
  const patchIndex = publishSource.indexOf('"PATCH"');
  const freshPublishedIndex = publishSource.indexOf("const freshPublished = readReleaseById");
  const postPublishHeadIndex = publishSource.indexOf("const postPublishRepository");
  assert.ok(
    rereadIndex >= 0 &&
      rereadIndex < prePublishHeadIndex &&
      prePublishHeadIndex < patchIndex &&
      patchIndex < freshPublishedIndex &&
      freshPublishedIndex < postPublishHeadIndex,
  );
  assert.ok(publishSource.indexOf("validatePublishedReleaseMetadata", patchIndex) > patchIndex);
});

test("local mutation path is draft-first and publication needs explicit confirmation", () => {
  assert.match(releaseLocal, /"release",\s*"create",[\s\S]*?"--draft"/);
  assert.match(releaseLocal, /options\.confirm !== options\.tag/);
  assert.match(releaseLocal, /options\.vmConfirm !== options\.tag/);
  assert.match(releaseLocal, /actions\/workflows\/release-verify\.yml/);
  assert.match(releaseLocal, /workflowRun\.head_sha !== verifierHead/);
  assert.doesNotMatch(releaseLocal, /workflowName|headBranch/);
});

test("publication authenticates verifier workflow path, ID, and exact head SHA", () => {
  const repository = { full_name: "openclaw/wacli", default_branch: "main" };
  const protectedBranch = { name: "main", protected: true, commit: { sha: verifierHead } };
  const workflow = { id: 7, path: ".github/workflows/release-verify.yml", state: "active" };
  const workflowRun = {
    id: 99,
    workflow_id: 7,
    path: ".github/workflows/release-verify.yml",
    event: "workflow_dispatch",
    status: "completed",
    conclusion: "success",
    head_branch: "main",
    head_sha: verifierHead,
    head_repository: { full_name: "openclaw/wacli" },
  };
  assert.doesNotThrow(() =>
    validateVerifierRun({
      repository,
      protectedBranch,
      workflow,
      workflowRun,
      runId: 99,
      verifierHead,
    }),
  );
  assert.throws(
    () =>
      validateVerifierRun({
        repository,
        protectedBranch,
        workflow,
        workflowRun: { ...workflowRun, workflow_id: 8 },
        runId: 99,
        verifierHead,
      }),
    /exact release-verify workflow/,
  );
  assert.throws(
    () =>
      validateVerifierRun({
        repository,
        protectedBranch,
        workflow,
        workflowRun: { ...workflowRun, path: ".github/workflows/other.yml" },
        runId: 99,
        verifierHead,
      }),
    /exact release-verify workflow/,
  );
  assert.throws(
    () =>
      validateVerifierRun({
        repository,
        protectedBranch,
        workflow,
        workflowRun: { ...workflowRun, head_sha: "c".repeat(40) },
        runId: 99,
        verifierHead,
      }),
    /exact protected-default/,
  );
  assert.throws(
    () =>
      validateVerifierRun({
        repository,
        protectedBranch: {
          ...protectedBranch,
          commit: { sha: "c".repeat(40) },
        },
        workflow,
        workflowRun,
        runId: 99,
        verifierHead,
      }),
    /current protected default-branch/,
  );
});

test("publication authenticates separate native verifier jobs and runner architectures", () => {
  const jobs = [
    {
      id: 100,
      name: "native-darwin-arm64",
      status: "completed",
      conclusion: "success",
      labels: ["macos-15"],
    },
    {
      id: 101,
      name: "native-darwin-x86_64",
      status: "completed",
      conclusion: "success",
      labels: ["macos-15-intel"],
    },
  ];
  assert.deepEqual([...validateVerifierJobs(jobs).keys()], ["arm64", "x86_64"]);
  assert.throws(
    () => validateVerifierJobs(jobs.map((job, index) => (index ? job : { ...job, labels: ["macos-15-intel"] }))),
    /native arm64 verifier job identity/,
  );
  assert.match(releaseLocal, /--job[\s\S]*String\(verifierJobs\.get\(arch\)\.id\)/);
  assert.match(releaseLocal, /native \$\{arch\} verifier job emitted the \$\{otherArch\} marker/);
});

test("publication cannot start before explicit clean-VM Gatekeeper proof", () => {
  const calls = [];
  assert.throws(
    () =>
      publishDraftRelease({
        releaseId: 42,
        tag,
        commit,
        verifierRun: 99,
        confirm: tag,
        run: (...args) => calls.push(args),
      }),
    /naturally quarantined clean-VM no-alert proof/,
  );
  assert.deepEqual(calls, []);
});

test("Homebrew handoff binds the public release ID, manifest, inventory, and prerelease state", () => {
  const assets = releaseAssetNames(version).map((name, index) => ({
    id: index + 1,
    name,
    size: index + 10,
    state: "uploaded",
    digest: `sha256:${String(index + 1).padStart(64, "0")}`,
  }));
  const release = {
    id: 42,
    tag_name: tag,
    target_commitish: commit,
    name: `wacli ${tag}`,
    body: expectedBody,
    draft: false,
    prerelease: false,
    published_at: "2026-07-09T12:00:00Z",
    assets,
  };
  const manifestDigest = releaseManifestDigest({ release_id: 42, tag, commit, assets });
  const options = { releaseId: 42, tag, commit, version, expectedBody, manifestDigest };
  assert.doesNotThrow(() => validateHomebrewRelease(release, options));
  assert.throws(
    () => validateHomebrewRelease({ ...release, prerelease: true }, options),
    /published, non-prerelease/,
  );
  assert.throws(
    () => validateHomebrewRelease(release, { ...options, manifestDigest: "f".repeat(64) }),
    /verified draft manifest/,
  );
  assert.throws(
    () => validateHomebrewRelease({ ...release, id: 43 }, options),
    /wrong published release ID/,
  );
  assert.doesNotMatch(releaseLocal, /releases\/tags\/\$\{options\.tag\}/);
});

test("Homebrew formula verification requires exact target URLs and checksums", () => {
  const checksums = new Map(
    archiveNames(version).map((name, index) => [name, String(index + 1).padStart(64, "0")]),
  );
  const pair = (target) => {
    const name = `wacli_${version}_${target}.tar.gz`;
    return (
      `      url "https://github.com/openclaw/wacli/releases/download/${tag}/${name}"\n` +
      `      sha256 "${checksums.get(name)}"`
    );
  };
  const formula =
    `class Wacli < Formula\n  version "${version}"\n` +
    `  on_macos do\n    if Hardware::CPU.arm?\n${pair("darwin_arm64")}\n    end\n\n` +
    `    if Hardware::CPU.intel?\n${pair("darwin_amd64")}\n    end\n  end\n` +
    `  on_linux do\n    if Hardware::CPU.arm? && Hardware::CPU.is_64_bit?\n` +
    `${pair("linux_arm64")}\n    end\n\n` +
    `    if Hardware::CPU.intel? && Hardware::CPU.is_64_bit?\n` +
    `${pair("linux_amd64")}\n    end\n  end\nend\n`;
  assert.doesNotThrow(() => validateHomebrewFormula(formula, { tag, checksums }));
  assert.throws(
    () =>
      validateHomebrewFormula(formula.replace(checksums.get(`wacli_${version}_darwin_arm64.tar.gz`), "f".repeat(64)), {
        tag,
        checksums,
      }),
    /checksum mismatch/,
  );
  assert.throws(
    () => validateHomebrewFormula(formula.replace(`/download/${tag}/`, "/download/v9.9.9/"), { tag, checksums }),
    /formula URL mismatch for darwin_arm64/,
  );
  const darwinArm64 = pair("darwin_arm64");
  const linuxArm64 = pair("linux_arm64");
  const swappedStanzas = formula
    .replace(darwinArm64, "__DARWIN_ARM64__")
    .replace(linuxArm64, darwinArm64)
    .replace("__DARWIN_ARM64__", linuxArm64);
  assert.throws(
    () => validateHomebrewFormula(swappedStanzas, { tag, checksums }),
    /formula URL mismatch for darwin_arm64/,
  );
  assert.throws(
    () =>
      validateHomebrewFormula(
        formula.replace(
          "if Hardware::CPU.intel? && Hardware::CPU.is_64_bit?",
          "if Hardware::CPU.intel?",
        ),
        { tag, checksums },
      ),
    /unsupported Homebrew CPU predicate for linux/,
  );
  assert.throws(
    () =>
      validateHomebrewFormula(
        formula.replace(
          "    end\n\n    if Hardware::CPU.intel?",
          "    else\n    end\n\n    if Hardware::CPU.intel?",
        ),
        { tag, checksums },
      ),
    /unsupported Homebrew target stanza structure: else/,
  );
});

test("installed Homebrew binary is the exact notarized Foundation release binary", () => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "wacli-homebrew-installed-test-"));
  try {
    const binary = path.join(directory, "wacli");
    fs.writeFileSync(binary, "signed release binary");
    const env = { HOME: directory, LANG: "C", LC_ALL: "C", PATH: "/usr/bin:/bin", TMPDIR: directory };
    const calls = [];
    const run = (command, args, options) => {
      calls.push([command, args, options]);
      assert.equal(options.env, env);
      if (command === "lipo") return { status: 0, stdout: "arm64\n", stderr: "" };
      if (command === "codesign" && args.includes("--requirements")) {
        return { status: 0, stdout: "", stderr: validRequirement() };
      }
      if (command === "codesign" && args.includes("--display")) {
        return { status: 0, stdout: "", stderr: validDisplay() };
      }
      if (command === "codesign") return { status: 0, stdout: "", stderr: "" };
      if (command === binary) return { status: 0, stdout: `wacli ${version}\n`, stderr: "" };
      throw new Error(`unexpected command: ${command} ${args.join(" ")}`);
    };
    assert.doesNotThrow(() =>
      verifyHomebrewInstalledBinary({
        binary,
        version,
        expectedSha256: sha256File(binary),
        expectedArch: "arm64",
        run,
        env,
      }),
    );
    assert.ok(
      calls.some(
        ([command, args]) =>
          command === "codesign" && args.includes("--check-notarization") && args.includes("-R=notarized"),
      ),
    );
    assert.throws(
      () =>
        verifyHomebrewInstalledBinary({
          binary,
          version,
          expectedSha256: "f".repeat(64),
          expectedArch: "arm64",
          run,
          env,
        }),
      /binary hash does not match/,
    );
    assert.throws(
      () =>
        verifyHomebrewInstalledBinary({
          binary,
          version,
          expectedSha256: sha256File(binary),
          expectedArch: "x86_64",
          run,
          env,
        }),
      /binary architecture inventory mismatch/,
    );
  } finally {
    fs.rmSync(directory, { recursive: true, force: true });
  }

  const handoffSource = releaseLocal.slice(
    releaseLocal.indexOf("export function dispatchHomebrewHandoff"),
    releaseLocal.indexOf("function required"),
  );
  const executionEnvIndex = handoffSource.indexOf("const executionEnv = sanitizedExecutionEnv()");
  const firstBrewIndex = handoffSource.indexOf('run("brew"');
  assert.ok(executionEnvIndex >= 0 && executionEnvIndex < firstBrewIndex);
  assert.ok(
    handoffSource.indexOf("assertNoReleaseCredentials(executionEnv)", executionEnvIndex) <
      firstBrewIndex,
  );
  const brewCalls = [...handoffSource.matchAll(/run\("brew",[\s\S]*?\);/g)].map(
    (match) => match[0],
  );
  assert.equal(brewCalls.length, 7);
  for (const call of brewCalls) assert.match(call, /env: executionEnv/);

  const installIndex = handoffSource.indexOf('run("brew", ["install"');
  const prefixIndex = handoffSource.indexOf('run("brew", ["--prefix"');
  const verifyIndex = handoffSource.indexOf("verifyHomebrewInstalledBinary({");
  const testIndex = handoffSource.indexOf('run("brew", ["test"');
  assert.ok(
    installIndex >= 0 &&
      installIndex < prefixIndex &&
      prefixIndex < verifyIndex &&
      verifyIndex < testIndex,
  );
});

test("Homebrew run authentication rejects a mutable workflow identity", () => {
  const tapHead = "e".repeat(40);
  const repository = { full_name: "openclaw/homebrew-tap", default_branch: "main" };
  const branch = { name: "main", protected: true, commit: { sha: tapHead } };
  const workflow = { id: 12, path: ".github/workflows/update-formula.yml", state: "active" };
  const workflowRun = {
    id: 99,
    workflow_id: 12,
    path: ".github/workflows/update-formula.yml",
    event: "workflow_dispatch",
    status: "completed",
    conclusion: "success",
    head_branch: "main",
    head_sha: tapHead,
    head_repository: { full_name: "openclaw/homebrew-tap" },
  };
  assert.doesNotThrow(() =>
    validateHomebrewRun({ repository, branch, workflow, workflowRun, runId: 99 }),
  );
  assert.equal(validateHomebrewBranch(repository, branch), tapHead);
  assert.throws(
    () => validateHomebrewBranch(repository, { ...branch, protected: false }),
    /not the protected default-branch head/,
  );
  assert.throws(
    () =>
      validateHomebrewRun({
        repository,
        branch,
        workflow,
        workflowRun: { ...workflowRun, path: ".github/workflows/other.yml" },
        runId: 99,
      }),
    /exact authenticated handoff workflow/,
  );
  const handoffSource = releaseLocal.slice(
    releaseLocal.indexOf("export function dispatchHomebrewHandoff"),
    releaseLocal.indexOf("function required"),
  );
  const preDispatchProtection = handoffSource.indexOf(
    "const branchHead = validateHomebrewBranch(repository, branch)",
  );
  const workflowDispatch = handoffSource.indexOf('"workflow",\n    "run"');
  const postWorkflowProtection = handoffSource.indexOf(
    "const updatedBranchHead = validateHomebrewBranch",
  );
  assert.ok(
    preDispatchProtection >= 0 &&
      preDispatchProtection < workflowDispatch &&
      postWorkflowProtection > workflowDispatch,
  );
});

test("Homebrew mutation cannot start without an explicit clean-host gate", () => {
  const calls = [];
  assert.throws(
    () =>
      dispatchHomebrewHandoff({
        releaseId: 42,
        tag,
        commit,
        manifestDigest: "f".repeat(64),
        run: (...args) => calls.push(args),
      }),
    /confirm-clean-homebrew-host/,
  );
  assert.deepEqual(calls, []);
});

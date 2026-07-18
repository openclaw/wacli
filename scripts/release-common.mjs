import crypto from "node:crypto";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

export const RELEASE_REPOSITORY = "openclaw/wacli";
export const RELEASE_GO_VERSION = "go1.25.12";
export const RELEASE_GO_TOOLCHAIN = "go1.25.12";
export const RELEASE_GOVULNCHECK_VERSION = "v1.5.0";
export const RELEASE_IDENTIFIER = "org.openclaw.wacli";
export const RELEASE_TEAM_ID = "FWJYW4S8P8";
export const RELEASE_AUTHORITY = `Developer ID Application: OpenClaw Foundation (${RELEASE_TEAM_ID})`;
export const RELEASE_DESIGNATED_REQUIREMENT =
  `designated => identifier "${RELEASE_IDENTIFIER}" and anchor apple generic ` +
  `and certificate leaf[subject.OU] = "${RELEASE_TEAM_ID}"`;

const repositoryRoot = path.dirname(fileURLToPath(new URL("../package.json", import.meta.url)));

const credentialNames = [
  "ACTIONS_ID_TOKEN_REQUEST_TOKEN",
  "ACTIONS_RUNTIME_TOKEN",
  "GH_TOKEN",
  "GITHUB_TOKEN",
  "HOMEBREW_GITHUB_API_TOKEN",
  "HOMEBREW_TAP_TOKEN",
];

export function versionFromTag(tag) {
  const match = /^v(\d+\.\d+\.\d+)$/.exec(String(tag ?? ""));
  if (!match) throw new Error(`release tag must look like vX.Y.Z, got ${JSON.stringify(tag)}`);
  return match[1];
}

export function assertCommit(commit) {
  if (!/^[0-9a-f]{40}$/.test(String(commit ?? ""))) {
    throw new Error("release commit must be a full lowercase 40-character SHA");
  }
}

export function archiveNames(version) {
  return [
    `wacli_${version}_darwin_amd64.tar.gz`,
    `wacli_${version}_darwin_arm64.tar.gz`,
    `wacli_${version}_darwin_universal.tar.gz`,
    `wacli_${version}_linux_amd64.tar.gz`,
    `wacli_${version}_linux_arm64.tar.gz`,
    `wacli_${version}_windows_amd64.zip`,
  ];
}

export function crossPlatformArchiveNames(version) {
  return archiveNames(version).slice(3);
}

export function releaseAssetNames(version) {
  return [...archiveNames(version), "checksums.txt"];
}

export function releaseArchiveTarget(name, version) {
  const targets = new Map([
    [`wacli_${version}_darwin_amd64.tar.gz`, { goos: "darwin", goarch: "amd64" }],
    [`wacli_${version}_darwin_arm64.tar.gz`, { goos: "darwin", goarch: "arm64" }],
    [`wacli_${version}_darwin_universal.tar.gz`, { goos: "darwin", goarch: "universal" }],
    [`wacli_${version}_linux_amd64.tar.gz`, { goos: "linux", goarch: "amd64" }],
    [`wacli_${version}_linux_arm64.tar.gz`, { goos: "linux", goarch: "arm64" }],
    [`wacli_${version}_windows_amd64.zip`, { goos: "windows", goarch: "amd64" }],
  ]);
  const target = targets.get(name);
  if (!target) throw new Error(`unknown release archive target ${name}`);
  return target;
}

export function assertExactInventory(actualNames, expectedNames, label = "asset") {
  const actual = [...actualNames];
  const expected = [...expectedNames];
  if (new Set(actual).size !== actual.length) {
    throw new Error(`${label} inventory contains duplicate names`);
  }
  actual.sort();
  expected.sort();
  if (actual.length !== expected.length || actual.some((name, index) => name !== expected[index])) {
    throw new Error(
      `${label} inventory mismatch: expected ${expected.join(", ")}; got ${actual.join(", ")}`,
    );
  }
}

export function parseChecksums(text, expectedNames) {
  const entries = new Map();
  const lines = String(text).split(/\r?\n/).filter(Boolean);
  for (const line of lines) {
    const match = /^([0-9a-f]{64}) [ *]([^/\\]+)$/.exec(line);
    if (!match) throw new Error(`malformed checksums.txt line: ${JSON.stringify(line)}`);
    const [, digest, name] = match;
    if (entries.has(name)) throw new Error(`duplicate checksum entry for ${name}`);
    entries.set(name, digest);
  }
  assertExactInventory(entries.keys(), expectedNames, "checksum");
  return entries;
}

export function sha256File(file) {
  const hash = crypto.createHash("sha256");
  hash.update(fs.readFileSync(file));
  return hash.digest("hex");
}

export function verifyChecksums(candidateDir, version) {
  const names = archiveNames(version);
  const entries = parseChecksums(
    fs.readFileSync(path.join(candidateDir, "checksums.txt"), "utf8"),
    names,
  );
  for (const name of names) {
    const actual = sha256File(path.join(candidateDir, name));
    if (actual !== entries.get(name)) {
      throw new Error(`checksum mismatch for ${name}: expected ${entries.get(name)}, got ${actual}`);
    }
  }
}

export function runCommand(command, args = [], options = {}) {
  const result = spawnSync(command, args, {
    cwd: options.cwd,
    env: options.env ?? process.env,
    encoding: "utf8",
    maxBuffer: options.maxBuffer ?? 16 * 1024 * 1024,
    stdio: options.stdio,
  });
  if (result.error) throw result.error;
  if (result.status !== 0 && !options.allowFailure) {
    const detail = [result.stdout, result.stderr].filter(Boolean).join("\n").trim();
    throw new Error(
      `${command} ${args.join(" ")} failed with exit ${result.status}${detail ? `:\n${detail}` : ""}`,
    );
  }
  return {
    status: result.status,
    stdout: result.stdout ?? "",
    stderr: result.stderr ?? "",
  };
}

export function assertNoReleaseCredentials(env = process.env) {
  const present = credentialNames.filter((name) => env[name]);
  if (present.length > 0) {
    throw new Error(`release verification refuses credential-bearing environment: ${present.join(", ")}`);
  }
}

export function sanitizedExecutionEnv(extra = {}, source = process.env) {
  const env = {
    HOME: source.HOME || os.homedir(),
    LANG: "C",
    LC_ALL: "C",
    PATH: source.PATH || "/usr/bin:/bin:/usr/sbin:/sbin",
    TMPDIR: source.TMPDIR || os.tmpdir(),
    ...extra,
  };
  for (const name of credentialNames) delete env[name];
  return env;
}

export function parseCodeSignatureDisplay(text) {
  const values = new Map();
  const authorities = [];
  for (const line of String(text).split(/\r?\n/)) {
    const index = line.indexOf("=");
    if (index < 1) continue;
    const key = line.slice(0, index).trim();
    const value = line.slice(index + 1).trim();
    if (key === "Authority") authorities.push(value);
    else values.set(key, value);
  }
  return { values, authorities };
}

export function assertCodeSignatureIdentity(displayText, requirementText) {
  const { values, authorities } = parseCodeSignatureDisplay(displayText);
  if (values.get("Identifier") !== RELEASE_IDENTIFIER) {
    throw new Error(`wrong signing identifier: ${values.get("Identifier") || "missing"}`);
  }
  if (values.get("TeamIdentifier") !== RELEASE_TEAM_ID) {
    throw new Error(`wrong signing team: ${values.get("TeamIdentifier") || "missing"}`);
  }
  const applicationAuthorities = authorities.filter((authority) =>
    authority.startsWith("Developer ID Application:"),
  );
  if (applicationAuthorities.length !== 1 || applicationAuthorities[0] !== RELEASE_AUTHORITY) {
    throw new Error(`signature authority is not exactly ${RELEASE_AUTHORITY}`);
  }
  if (!/\bflags=0x[0-9a-f]+\(runtime\)/i.test(displayText)) {
    throw new Error("signature is missing hardened runtime");
  }
  if (!/^Timestamp=(?!none$).+/m.test(displayText)) {
    throw new Error("signature is missing a trusted timestamp");
  }

  const normalizeRequirement = (text) =>
    String(text)
      .replace(/\s+/g, " ")
      // codesign's display normalization drops quotes around alphanumeric
      // values (leaf[subject.OU] = FWJYW4S8P8 vs = "FWJYW4S8P8"); compare
      // both sides without them so the check is not display-format-brittle.
      .replace(/"([A-Za-z0-9.]+)"/g, "$1")
      // some codesign versions append the "Executable=..." header line to
      // the requirements output; it is not part of the requirement itself.
      .replace(/ Executable=.*$/, "")
      .trim();
  const requirement = normalizeRequirement(requirementText);
  if (requirement !== normalizeRequirement(RELEASE_DESIGNATED_REQUIREMENT)) {
    throw new Error(`embedded designated requirement mismatch: ${JSON.stringify(requirement)}`);
  }
}

export function releaseManifestDigest(metadata) {
  const canonical = {
    release_id: Number(metadata.release_id),
    tag: metadata.tag,
    commit: metadata.commit,
    assets: [...metadata.assets]
      .map(({ id, name, size, digest }) => ({
        id: Number(id),
        name,
        size: Number(size),
        digest: digest ?? null,
      }))
      .sort((left, right) => left.name.localeCompare(right.name)),
  };
  return crypto.createHash("sha256").update(JSON.stringify(canonical)).digest("hex");
}

export function verifyDarwinSignature(binary, options = {}) {
  const run = options.run ?? runCommand;
  const archArgs = options.arch ? ["--arch", options.arch] : [];
  run("codesign", ["--verify", "--strict", "--verbose=4", ...archArgs, binary]);
  const display = run("codesign", ["--display", "--verbose=4", ...archArgs, binary]);
  const requirement = run("codesign", [
    "--display",
    "--requirements",
    "-",
    ...archArgs,
    binary,
  ]);
  const displayText = `${display.stdout ?? ""}${display.stderr ?? ""}`;
  const requirementText = `${requirement.stdout ?? ""}${requirement.stderr ?? ""}`;
  assertCodeSignatureIdentity(displayText, requirementText);

  if (options.requireNotarization !== false) {
    run("codesign", [
      "--verify",
      "--strict",
      "--check-notarization",
      "-R=notarized",
      ...archArgs,
      binary,
    ]);
  }
}

export function parseArchiveListing(text) {
  const names = [];
  for (const raw of String(text).split(/\r?\n/).filter(Boolean)) {
    const name = raw.replace(/^\.\//, "").replace(/\/$/, "");
    if (!name || name.startsWith("/") || name.split("/").includes("..")) {
      throw new Error(`unsafe archive entry: ${JSON.stringify(raw)}`);
    }
    names.push(name);
  }
  return names;
}

export function assertArchiveContents(archive, expectedBinary, options = {}) {
  const run = options.run ?? runCommand;
  const result = archive.endsWith(".zip")
    ? run("unzip", ["-Z1", archive])
    : run("tar", ["-tzf", archive]);
  const names = parseArchiveListing(`${result.stdout ?? ""}${result.stderr ?? ""}`);
  assertExactInventory(names, ["LICENSE", "README.md", expectedBinary], "archive entry");
}

export function inspectLinkedReleaseVersion(binary, options = {}) {
  const run = options.run ?? runCommand;
  const env = sanitizedExecutionEnv(
    { GOTOOLCHAIN: RELEASE_GO_TOOLCHAIN },
    options.env ?? process.env,
  );
  assertNoReleaseCredentials(env);
  const result = run(
    "go",
    ["run", "./scripts/release-version-inspector", path.resolve(binary)],
    { cwd: repositoryRoot, env },
  );
  let inspected;
  try {
    inspected = JSON.parse(result.stdout);
  } catch {
    throw new Error(`${path.basename(binary)} has malformed linked release-version metadata`);
  }
  if (
    !inspected ||
    typeof inspected !== "object" ||
    Array.isArray(inspected) ||
    typeof inspected.version !== "string" ||
    typeof inspected.releaseLinkerSetting !== "string" ||
    Object.keys(inspected).sort().join(",") !== "releaseLinkerSetting,version"
  ) {
    throw new Error(`${path.basename(binary)} has malformed linked release-version metadata`);
  }
  return inspected;
}

export function assertGoBuildInfo(binary, version, options = {}) {
  const run = options.run ?? runCommand;
  assertCommit(options.commit);
  if (!options.expectedGoos || !options.expectedGoarch) {
    throw new Error("expected GOOS and GOARCH are required for release build verification");
  }
  const result = run("go", ["version", "-m", "-json", binary], {
    env: sanitizedExecutionEnv({}, options.env ?? process.env),
  });
  let buildInfo;
  try {
    buildInfo = JSON.parse(result.stdout);
  } catch {
    throw new Error(`${path.basename(binary)} has malformed Go build information`);
  }
  if (buildInfo.GoVersion !== RELEASE_GO_VERSION) {
    throw new Error(
      `${path.basename(binary)} was built with ${buildInfo.GoVersion ?? "unknown"}, not ${RELEASE_GO_VERSION}`,
    );
  }
  if (buildInfo.Path !== "github.com/openclaw/wacli/cmd/wacli") {
    throw new Error(`${path.basename(binary)} has the wrong Go main package`);
  }
  const settings = new Map();
  for (const setting of buildInfo.Settings ?? []) {
    if (settings.has(setting.Key)) throw new Error(`duplicate Go build setting ${setting.Key}`);
    settings.set(setting.Key, setting.Value);
  }
  if (settings.get("GOOS") !== options.expectedGoos || settings.get("GOARCH") !== options.expectedGoarch) {
    throw new Error(
      `${path.basename(binary)} target mismatch: expected ${options.expectedGoos}/${options.expectedGoarch}, ` +
        `got ${settings.get("GOOS") ?? "unknown"}/${settings.get("GOARCH") ?? "unknown"}`,
    );
  }
  if (settings.get("vcs.revision") !== options.commit) {
    throw new Error(`${path.basename(binary)} was not built from release commit ${options.commit}`);
  }
  if (settings.get("vcs.modified") !== "false") {
    throw new Error(`${path.basename(binary)} was built from a modified checkout`);
  }
  if (settings.get("CGO_ENABLED") !== "1" || settings.get("-tags") !== "sqlite_fts5") {
    throw new Error(`${path.basename(binary)} is missing the exact cgo/sqlite_fts5 release settings`);
  }
  if (settings.get("-trimpath") !== "true") {
    throw new Error(`${path.basename(binary)} is missing the reproducible -trimpath release setting`);
  }
  const linked = inspectLinkedReleaseVersion(binary, { run, env: options.env });
  if (linked.version !== version) {
    throw new Error(
      `${path.basename(binary)} has linked runtime version ${JSON.stringify(linked.version)}, ` +
        `not ${JSON.stringify(version)}`,
    );
  }
  const expectedLinkerSetting = `wacli-release-linker-version=[${version}]`;
  if (linked.releaseLinkerSetting !== expectedLinkerSetting) {
    throw new Error(
      `${path.basename(binary)} has linked release marker ` +
        `${JSON.stringify(linked.releaseLinkerSetting)}, not ${JSON.stringify(expectedLinkerSetting)}`,
    );
  }
  if (options.verifyRuntimeVersion === true) {
    assertRuntimeVersion(binary, version, { run, env: options.env });
  }
}

export function assertRuntimeVersion(binary, version, options = {}) {
  const run = options.run ?? runCommand;
  const env = options.env ?? sanitizedExecutionEnv();
  assertNoReleaseCredentials(env);
  const result = run(binary, ["--version"], {
    cwd: path.dirname(binary),
    env,
  });
  const output = `${result.stdout ?? ""}${result.stderr ?? ""}`.trim();
  if (output !== `wacli ${version}`) {
    throw new Error(`${path.basename(binary)} --version returned ${JSON.stringify(output)}`);
  }
}

export function parseCliArgs(argv) {
  const args = {};
  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index];
    if (!arg.startsWith("--")) throw new Error(`unexpected argument ${arg}`);
    const key = arg.slice(2);
    const value = argv[index + 1];
    if (!value || value.startsWith("--")) throw new Error(`missing value for ${arg}`);
    args[key] = value;
    index += 1;
  }
  return args;
}

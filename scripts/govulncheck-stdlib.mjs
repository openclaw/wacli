#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { RELEASE_GOVULNCHECK_VERSION } from "./release-common.mjs";

export function parseJsonStream(text) {
  const values = [];
  let start = -1;
  let depth = 0;
  let inString = false;
  let escaped = false;
  for (let index = 0; index < text.length; index += 1) {
    const character = text[index];
    if (start < 0) {
      if (/\s/.test(character)) continue;
      if (character !== "{") throw new Error(`unexpected govulncheck JSON byte ${JSON.stringify(character)}`);
      start = index;
      depth = 1;
      continue;
    }
    if (inString) {
      if (escaped) escaped = false;
      else if (character === "\\") escaped = true;
      else if (character === '"') inString = false;
      continue;
    }
    if (character === '"') inString = true;
    else if (character === "{") depth += 1;
    else if (character === "}") {
      depth -= 1;
      if (depth === 0) {
        values.push(JSON.parse(text.slice(start, index + 1)));
        start = -1;
      }
    }
  }
  if (start >= 0 || inString || depth !== 0) throw new Error("truncated govulncheck JSON stream");
  return values;
}

export function classifyGovulncheckEvents(events) {
  const advisories = new Map();
  const findings = new Map();
  for (const event of events) {
    if (event.osv) advisories.set(event.osv.id, event.osv);
    if (!event.finding) continue;
    const id = event.finding.osv;
    const moduleLevels = findings.get(id) ?? new Map();
    const level = (event.finding.trace ?? []).some((frame) => frame.function)
      ? "symbol"
      : (event.finding.trace ?? []).some((frame) => frame.package)
        ? "package"
        : "module";
    // The first frame owns the vulnerable symbol; later frames are callers.
    // A stdlib caller does not turn a third-party finding into a stdlib one.
    const frame = event.finding.trace?.[0];
    const module = frame?.module || (frame?.version?.startsWith("go") && frame.package ? "stdlib" : null);
    if (module) {
      const levels = moduleLevels.get(module) ?? new Set();
      levels.add(level);
      moduleLevels.set(module, levels);
    }
    findings.set(id, moduleLevels);
  }

  const active = [...findings].map(([id, moduleLevels]) => {
    const modules = [...moduleLevels.keys()].sort();
    const levels = [...new Set([...moduleLevels.values()].flatMap((values) => [...values]))].sort();
    return {
      id,
      modules,
      levels,
      moduleLevels,
      summary: advisories.get(id)?.summary ?? "summary unavailable",
    };
  });
  active.sort((left, right) => left.id.localeCompare(right.id));
  const unclassified = active.filter((finding) => finding.modules.length === 0);
  const stdlib = active.filter((finding) => finding.moduleLevels.get("stdlib")?.has("symbol"));
  const stdlibNonReachable = active.filter(
    (finding) => finding.modules.includes("stdlib") && !finding.moduleLevels.get("stdlib")?.has("symbol"),
  );
  const thirdParty = active.filter((finding) => finding.modules.some((module) => module !== "stdlib"));
  return { active, stdlib, stdlibNonReachable, thirdParty, unclassified };
}

export function formatGateResult(result) {
  const lines = [];
  for (const finding of result.active) {
    lines.push(
      `REPORTED ${finding.id} levels=${finding.levels.join(",") || "unclassified"} ` +
        `modules=${finding.modules.join(",") || "unclassified"} summary=${finding.summary}`,
    );
  }
  if (result.thirdParty.length > 0) {
    lines.push(
      `NOTICE non-stdlib findings remain reported and unsuppressed: ${result.thirdParty
        .map((finding) => finding.id)
        .join(", ")}`,
    );
  }
  if (result.stdlib.length === 0 && result.unclassified.length === 0) {
    lines.push("stdlib govulncheck gate clean: no reachable standard-library vulnerabilities");
  }
  return lines.join("\n");
}

export function runGate(mode, target, options = {}) {
  const args = [
    "run",
    `golang.org/x/vuln/cmd/govulncheck@${RELEASE_GOVULNCHECK_VERSION}`,
    "-format=json",
  ];
  if (mode === "source") args.push("-tags", "sqlite_fts5", "./...");
  else if (mode === "binary") args.push("-mode=binary", target);
  else throw new Error("mode must be source or binary");

  const result = spawnSync("go", args, {
    cwd: options.cwd,
    env: options.env ?? process.env,
    encoding: "utf8",
    maxBuffer: 128 * 1024 * 1024,
  });
  if (result.error) throw result.error;
  const events = parseJsonStream(result.stdout ?? "");
  const classified = classifyGovulncheckEvents(events);
  const expectedFindingExit = classified.active.length > 0 && /exit status 3/.test(result.stderr ?? "");
  if (result.status !== 0 && !expectedFindingExit) {
    throw new Error(`govulncheck failed: ${(result.stderr ?? "").trim() || `exit ${result.status}`}`);
  }
  if (classified.unclassified.length > 0) {
    throw new Error(
      `govulncheck findings could not be classified: ${classified.unclassified
        .map((finding) => finding.id)
        .join(", ")}`,
    );
  }
  if (classified.stdlib.length > 0) {
    throw new Error(
      `reachable standard-library vulnerabilities: ${classified.stdlib
        .map((finding) => finding.id)
        .join(", ")}`,
    );
  }
  return classified;
}

function main() {
  const [mode, target] = process.argv.slice(2);
  if (mode === "binary" && !target) throw new Error("binary mode requires a file");
  if (mode === "source" && target) throw new Error("source mode takes no target");
  const result = runGate(mode, target, { cwd: process.cwd() });
  process.stdout.write(`${formatGateResult(result)}\n`);
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  try {
    main();
  } catch (error) {
    process.stderr.write(`stdlib govulncheck gate failed: ${error.message}\n`);
    process.exitCode = 1;
  }
}

import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";

const workflow = fs.readFileSync(new URL("../.github/workflows/release.yml", import.meta.url), "utf8");

test("historical release fallback preserves the full checkout", () => {
  const fallback = workflow.match(/if \[ ! -f "\$helper" \]; then([\s\S]*?)\n\s+fi/);
  assert.ok(fallback, "release-note fallback block is missing");
  assert.match(fallback[1], /git fetch --no-tags origin main/);
  assert.doesNotMatch(fallback[1], /--depth(?:=|\s)/);
});

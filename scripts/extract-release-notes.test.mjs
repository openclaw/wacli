import assert from "node:assert/strict";
import test from "node:test";

import { extractReleaseNotes } from "./extract-release-notes.mjs";

test("extractReleaseNotes returns only the dated target section", () => {
  const changelog = `# Changelog

## 1.2.4 - Unreleased

### Fixed

- Next fix.

## 1.2.3 - 2026-07-01

### Fixed

- Released fix.

## 1.2.2 - 2026-06-01

- Older fix.
`;

  assert.equal(extractReleaseNotes(changelog, "v1.2.3"), "## Changelog\n\n### Fixed\n\n- Released fix.\n");
});

test("extractReleaseNotes rejects an unreleased target section", () => {
  assert.throws(
    () => extractReleaseNotes("## 1.2.3 - Unreleased\n\n- Pending.\n", "v1.2.3"),
    /still Unreleased/,
  );
});

test("extractReleaseNotes rejects a missing target section", () => {
  assert.throws(() => extractReleaseNotes("# Changelog\n", "v1.2.3"), /missing changelog section/);
});

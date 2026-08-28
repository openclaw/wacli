import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { RELEASE_GO_TOOLCHAIN, RELEASE_GO_VERSION } from "./release-common.mjs";

const read = (file) => readFileSync(new URL(`../${file}`, import.meta.url), "utf8");

test("build and release gates use the Go version declared in go.mod", () => {
  const version = read("go.mod").match(/^go (\S+)$/m)[1];
  assert.equal(RELEASE_GO_VERSION, `go${version}`);
  assert.equal(RELEASE_GO_TOOLCHAIN, `go${version}`);
  assert.equal(read("Makefile").match(/GOVERSION\)" = (go\S+)/)[1], `go${version}`);
  assert.equal(read(".github/workflows/ci.yml").match(/GOVERSION\)" = "(go[^"]+)"/)[1], `go${version}`);
  assert.equal(read("Dockerfile").match(/FROM golang:([\d.]+)-alpine@/)[1], version);
});

test("pnpm setup uses the package manifest and hydration pins the same version", () => {
  const version = JSON.parse(read("package.json")).packageManager.match(/^pnpm@([^+]+)/)[1];
  const setup = read(".github/actions/setup-ci-env/action.yml");
  assert.match(setup, /uses: pnpm\/action-setup@/);
  assert.doesNotMatch(setup, /corepack (enable|prepare)/);
  assert.equal(read(".github/workflows/crabbox-hydrate.yml").match(/PNPM_VERSION: "([^"]+)"/)[1], version);
});

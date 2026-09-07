import assert from "node:assert/strict";
import test from "node:test";
import { releaseGoVersionForCommit } from "./release-common.mjs";

const commit = "a".repeat(40);
const resolve = (module) => releaseGoVersionForCommit(commit, {
  run(command, args) {
    assert.equal(command, "git");
    assert.deepEqual(args, ["show", `${commit}:go.mod`]);
    return { stdout: module };
  },
});

test("release verification uses the frozen commit's preferred toolchain", () => {
  assert.equal(resolve("module example.com/cli\n\ngo 1.27.0\n\ntoolchain go1.27.1\n"), "go1.27.1");
});

test("historical release commits without a toolchain retain their exact Go version", () => {
  assert.equal(resolve("module example.com/cli\n\ngo 1.26.6\n"), "go1.26.6");
});

test("release verification rejects ambiguous or non-exact toolchains", () => {
  for (const directive of ["default", "go1.27", "go1.27.1\ntoolchain go1.27.2"]) {
    assert.throws(() => resolve(`go 1.27.0\ntoolchain ${directive}\n`), /exact Go toolchain/);
  }
});

test("release toolchain cannot be older than the module minimum", () => {
  assert.throws(() => resolve("go 1.27.0\ntoolchain go1.26.8\n"), /older than/);
});

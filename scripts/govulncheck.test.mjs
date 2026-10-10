import assert from "node:assert/strict";
import test from "node:test";
import { classifyGovulncheckEvents } from "./govulncheck-stdlib.mjs";

const finding = (trace) => ({ finding: { osv: "GO-TEST-0001", trace } });

test("a third-party vulnerability called through stdlib is not a stdlib vulnerability", () => {
  const result = classifyGovulncheckEvents([finding([
    { module: "golang.org/x/net", version: "v0.59.0", package: "golang.org/x/net/http2", function: "Transport.RoundTrip" },
    { module: "stdlib", version: "go1.27.2", package: "net/http", function: "Client.Do" },
    { module: "example.com/app", package: "example.com/app", function: "main" },
  ])]);
  assert.deepEqual(result.active[0].modules, ["golang.org/x/net"]);
  assert.equal(result.stdlib.length, 0);
  assert.equal(result.thirdParty.length, 1);
});

test("a stdlib vulnerability stays blocking when called by another module", () => {
  const result = classifyGovulncheckEvents([finding([
    { module: "stdlib", version: "go1.27.1", package: "net/http", function: "Client.Do" },
    { module: "example.com/app", package: "example.com/app", function: "main" },
  ])]);
  assert.deepEqual(result.active[0].modules, ["stdlib"]);
  assert.equal(result.stdlib.length, 1);
  assert.equal(result.thirdParty.length, 0);
});

test("one advisory can have separate stdlib and third-party findings", () => {
  const result = classifyGovulncheckEvents([
    finding([{ module: "stdlib", version: "go1.27.1", package: "net/http", function: "Client.Do" }]),
    finding([{ module: "golang.org/x/net", version: "v0.59.0", package: "golang.org/x/net/http2", function: "Transport.RoundTrip" }]),
  ]);
  assert.equal(result.stdlib.length, 1);
  assert.equal(result.thirdParty.length, 1);
});

test("a missing vulnerable module is not inferred from a caller", () => {
  const result = classifyGovulncheckEvents([finding([
    { package: "unknown", function: "vulnerable" },
    { module: "stdlib", version: "go1.27.2", package: "net/http", function: "Client.Do" },
  ])]);
  assert.equal(result.unclassified.length, 1);
});

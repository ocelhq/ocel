import assert from "node:assert/strict";
import { afterEach, describe, it } from "vitest";
import { EDGE_ENV } from "./aws.floci";
import { expectationsFor } from "./index";

const before = process.env[EDGE_ENV];

afterEach(() => {
  if (before === undefined) {
    delete process.env[EDGE_ENV];
  } else {
    process.env[EDGE_ENV] = before;
  }
});

describe("expectationsFor", () => {
  it("does not read the edge for an environment that has no edges", () => {
    process.env[EDGE_ENV] = "not-an-edge";
    assert.doesNotThrow(() => expectationsFor("dev"));
    assert.doesNotThrow(() => expectationsFor("aws"));
  });

  it("lists nothing on real aws but the pre-declared edge-runtime ISR red", () => {
    const listed = expectationsFor("aws");
    assert.deepEqual(Object.keys(listed), ["next/web"]);
    const titles = Object.values(listed["next/web"] ?? {});
    assert.equal(titles.length, 3);
    assert.deepEqual(new Set(titles), new Set(["https://github.com/ocelhq/ocel/issues/899"]));
  });

  it("names the variable when the aws floci file is asked for an edge it does not list", () => {
    process.env[EDGE_ENV] = "not-an-edge";
    assert.throws(() => expectationsFor("aws.floci"), new RegExp(EDGE_ENV));
  });

  it("lists every planned title for an edge it knows", () => {
    process.env[EDGE_ENV] = "api-gateway";
    const listed = expectationsFor("aws.floci");
    const cell = listed["express/web"];
    assert.ok(cell);
    assert.equal(cell.up, "https://github.com/ocelhq/ocel/issues/884");
    assert.equal(
      cell["GET /health answers with the app name"],
      "https://github.com/ocelhq/ocel/issues/854",
    );
    assert.equal(
      cell["GET /api/probes/stream streams its chunks in order to the sentinel"],
      "https://github.com/ocelhq/ocel/issues/851",
    );
    assert.equal(
      cell["redeploy · GET /health answers with the app name"],
      "https://github.com/ocelhq/ocel/issues/854",
    );
  });

  it("never lists a ladder's refuse title, on any edge", () => {
    for (const edge of ["api-gateway", "cloudfront", "cloudflare"]) {
      process.env[EDGE_ENV] = edge;
      const listed = expectationsFor("aws.floci");
      assert.equal(listed["with-sst/web"]?.refuse, undefined, edge);
      assert.equal(listed["with-pulumi/web"]?.refuse, undefined, edge);
    }
  });

  it("lists with-sst red from publish onward under its own issue, not the edge's", () => {
    const publishTitle = "publish · ocel link ls lists both records with their name, type, source and owner";
    for (const edge of ["api-gateway", "cloudfront", "cloudflare"]) {
      process.env[EDGE_ENV] = edge;
      const cell = expectationsFor("aws.floci")["with-sst/web"];
      assert.ok(cell, edge);
      assert.equal(cell![publishTitle], "https://github.com/ocelhq/ocel/issues/857", edge);
      assert.equal(cell!.up, "https://github.com/ocelhq/ocel/issues/857", edge);
    }
  });

  it("lists with-pulumi red from publish onward, under its own issue", () => {
    process.env[EDGE_ENV] = "api-gateway";
    const cell = expectationsFor("aws.floci")["with-pulumi/web"];
    assert.ok(cell);
    assert.equal(cell!.up, "https://github.com/ocelhq/ocel/issues/856");
  });

  it("leaves with-transforms to the edge's own reason, not a ladder issue", () => {
    process.env[EDGE_ENV] = "api-gateway";
    const cell = expectationsFor("aws.floci")["with-transforms/web"];
    assert.ok(cell);
    assert.equal(cell!.up, "https://github.com/ocelhq/ocel/issues/884");
  });
});

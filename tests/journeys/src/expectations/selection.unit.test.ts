import assert from "node:assert/strict";
import { afterEach, describe, it } from "vitest";
import { EDGE_ISR_TITLE, nextCacheRows } from "../nextCache";
import { contractTitle } from "../plan";
import { EDGE_ENV } from "./aws.floci";
import { CONTRACT_LEGS } from "./keys";
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

  it("lists every contract title on api-gateway, and up under the master-secret issue", () => {
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

  it("lists no leg marker on api-gateway: a failed up blocks the legs behind it", () => {
    process.env[EDGE_ENV] = "api-gateway";
    const cell = expectationsFor("aws.floci")["express/web"];
    assert.ok(cell);
    assert.equal(cell.redeploy, undefined);
    assert.equal(cell.rollback, undefined);
    assert.equal(cell.destroy, undefined);
  });

  it("lists up alone under the edge's own issue on cloudfront and cloudflare", () => {
    const issues: Record<string, string> = {
      cloudfront: "https://github.com/ocelhq/ocel/issues/852",
      cloudflare: "https://github.com/ocelhq/ocel/issues/904",
    };
    for (const [edge, issue] of Object.entries(issues)) {
      process.env[EDGE_ENV] = edge;
      const listed = expectationsFor("aws.floci");
      const cell = listed["express/web"];
      assert.ok(cell, edge);
      assert.deepEqual(Object.keys(cell), ["up"], edge);
      assert.equal(cell.up, issue, edge);
      assert.deepEqual(Object.keys(listed["next/web"] ?? {}), ["up"], edge);
    }
  });

  it("never lists a ladder's refuse or publish title, on any edge", () => {
    const publishTitle =
      "publish · ocel link ls lists both records with their name, type, source and owner";
    for (const edge of ["api-gateway", "cloudfront", "cloudflare"]) {
      process.env[EDGE_ENV] = edge;
      const listed = expectationsFor("aws.floci");
      assert.equal(listed["with-sst/web"]?.refuse, undefined, edge);
      assert.equal(listed["with-pulumi/web"]?.refuse, undefined, edge);
      assert.equal(listed["with-sst/web"]?.[publishTitle], undefined, edge);
      assert.equal(listed["with-pulumi/web"]?.[publishTitle], undefined, edge);
    }
  });

  it("lists each ladder at up under its own issue, not the edge's", () => {
    for (const edge of ["api-gateway", "cloudfront", "cloudflare"]) {
      process.env[EDGE_ENV] = edge;
      const listed = expectationsFor("aws.floci");
      assert.equal(listed["with-sst/web"]?.up, "https://github.com/ocelhq/ocel/issues/857", edge);
      assert.equal(listed["with-pulumi/web"]?.up, "https://github.com/ocelhq/ocel/issues/856", edge);
    }
  });

  it("leaves with-transforms to the edge's own reason, not a ladder issue", () => {
    process.env[EDGE_ENV] = "api-gateway";
    const cell = expectationsFor("aws.floci")["with-transforms/web"];
    assert.ok(cell);
    assert.equal(cell.up, "https://github.com/ocelhq/ocel/issues/884");
  });

  it("lists each next-cache title on api-gateway by the exact title the plan gives it", () => {
    process.env[EDGE_ENV] = "api-gateway";
    const cell = expectationsFor("aws.floci")["next/web"];
    assert.ok(cell);
    for (const row of nextCacheRows) {
      const issue =
        row.title === EDGE_ISR_TITLE
          ? "https://github.com/ocelhq/ocel/issues/899"
          : "https://github.com/ocelhq/ocel/issues/854";
      for (const leg of CONTRACT_LEGS) {
        assert.equal(
          cell[contractTitle(leg, row.title)],
          issue,
          `floci lists no issue for ${contractTitle(leg, row.title)}`,
        );
      }
    }
  });
});

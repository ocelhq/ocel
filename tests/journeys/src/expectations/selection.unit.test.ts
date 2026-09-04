import assert from "node:assert/strict";
import { afterEach, describe, it } from "vitest";
import { EDGE_ISR_TITLE, nextCacheRows } from "../nextCache";
import { contractTitle } from "../plan";
import { EDGE_ENV } from "./edge";
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
  });

  it("names the variable when an aws file is asked for an edge it does not list", () => {
    process.env[EDGE_ENV] = "not-an-edge";
    assert.throws(() => expectationsFor("aws"), new RegExp(EDGE_ENV));
    assert.throws(() => expectationsFor("aws.floci"), new RegExp(EDGE_ENV));
  });

  it("lists the same real-world up on every edge for the cells that fail everywhere", () => {
    const everywhere: Record<string, string> = {
      "express/web": "https://github.com/ocelhq/ocel/issues/911",
      "hono/web": "https://github.com/ocelhq/ocel/issues/911",
      "fastify/web": "https://github.com/ocelhq/ocel/issues/911",
      "next/web": "https://github.com/ocelhq/ocel/issues/849",
      "workspace/next": "https://github.com/ocelhq/ocel/issues/907",
      "workspace/express": "https://github.com/ocelhq/ocel/issues/907",
      "workspace/hono": "https://github.com/ocelhq/ocel/issues/907",
      "with-sst/web": "https://github.com/ocelhq/ocel/issues/857",
      "with-pulumi/web": "https://github.com/ocelhq/ocel/issues/856",
    };
    for (const edge of ["api-gateway", "cloudfront", "cloudflare"]) {
      process.env[EDGE_ENV] = edge;
      const listed = expectationsFor("aws");
      for (const [cell, issue] of Object.entries(everywhere)) {
        assert.deepEqual(listed[cell], { up: issue }, `${cell} on ${edge}`);
      }
    }
  });

  it("lists real-world hello-next and not hello-express on api-gateway", () => {
    process.env[EDGE_ENV] = "api-gateway";
    const listed = expectationsFor("aws");
    assert.deepEqual(listed["hello-next/web"], { up: "https://github.com/ocelhq/ocel/issues/906" });
    assert.deepEqual(listed["hello-express/web"], {});
    assert.deepEqual(listed["with-transforms/web"], {
      "GET /api/link/query answers ok after a select through the link":
        "https://github.com/ocelhq/ocel/issues/925",
      "redeploy · GET /api/link/query answers ok after a select through the link":
        "https://github.com/ocelhq/ocel/issues/925",
      "rollback · GET /api/link/query answers ok after a select through the link":
        "https://github.com/ocelhq/ocel/issues/925",
      "redeploy · GET /api/link answers with what it resolved and the greeting it deployed with":
        "https://github.com/ocelhq/ocel/issues/926",
    });
  });

  it("lists every remaining real-world cell at up under the edge's own issue", () => {
    const issues: Record<string, string> = {
      cloudfront: "https://github.com/ocelhq/ocel/issues/923",
      cloudflare: "https://github.com/ocelhq/ocel/issues/922",
    };
    for (const [edge, issue] of Object.entries(issues)) {
      process.env[EDGE_ENV] = edge;
      const listed = expectationsFor("aws");
      for (const cell of ["hello-express/web", "hello-next/web", "with-transforms/web"]) {
        assert.deepEqual(listed[cell], { up: issue }, `${cell} on ${edge}`);
      }
    }
  });

  it("lists no contract title under a listed up on real aws: the up covers the cell behind it", () => {
    for (const edge of ["api-gateway", "cloudfront", "cloudflare"]) {
      process.env[EDGE_ENV] = edge;
      for (const [name, cell] of Object.entries(expectationsFor("aws"))) {
        if (!("up" in cell)) {
          continue;
        }
        const contract = Object.keys(cell).filter((title) => title !== "up");
        assert.deepEqual(contract, [], `${name} on ${edge}`);
      }
    }
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

  it("leaves with-transforms to the master-secret issue, not a ladder issue", () => {
    process.env[EDGE_ENV] = "api-gateway";
    const cell = expectationsFor("aws.floci")["with-transforms/web"];
    assert.ok(cell);
    assert.equal(cell.up, "https://github.com/ocelhq/ocel/issues/884");
  });

  it("names each api-gateway up after the reason that cell actually fails for", () => {
    process.env[EDGE_ENV] = "api-gateway";
    const listed = expectationsFor("aws.floci");
    const upIssues: Record<string, string | undefined> = {
      "hono/web": "https://github.com/ocelhq/ocel/issues/884",
      "fastify/web": "https://github.com/ocelhq/ocel/issues/884",
      "next/web": "https://github.com/ocelhq/ocel/issues/849",
      "hello-next/web": "https://github.com/ocelhq/ocel/issues/906",
      "workspace/next": "https://github.com/ocelhq/ocel/issues/907",
      "workspace/express": "https://github.com/ocelhq/ocel/issues/907",
      "workspace/hono": "https://github.com/ocelhq/ocel/issues/907",
    };
    for (const [cell, issue] of Object.entries(upIssues)) {
      assert.equal(listed[cell]?.up, issue, cell);
    }
  });

  it("lists no up for hello-express on api-gateway, and keeps its contract titles", () => {
    process.env[EDGE_ENV] = "api-gateway";
    const cell = expectationsFor("aws.floci")["hello-express/web"];
    assert.ok(cell);
    assert.equal(cell.up, undefined);
    assert.equal(
      cell["GET /health answers with the app name"],
      "https://github.com/ocelhq/ocel/issues/854",
    );
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

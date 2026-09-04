import assert from "node:assert/strict";
import { afterEach, describe, it } from "vitest";
import { EDGE_ISR_TITLE, nextCacheRows } from "../nextCache";
import { contractTitle, DESTROY_TITLE, UP_TITLE } from "../plan";
import { gaps } from "./gaps";
import { CONTRACT_LEGS, EDGE_ENV, EDGES, type Expectations, expectationsFor } from "./index";

const before = process.env[EDGE_ENV];

afterEach(() => {
  if (before === undefined) {
    delete process.env[EDGE_ENV];
  } else {
    process.env[EDGE_ENV] = before;
  }
});

const HEALTH = "GET /health answers with the app name";
const STREAM = "GET /api/probes/stream streams its chunks in order to the sentinel";
const UPLOAD = "the upload protocol stores a document and /api/documents lists it";

function issues(listed: Expectations, cell: string, title: string): number[] {
  return (listed[cell]?.[title] ?? []).map((gap) => gap.issue ?? Number.NaN);
}

function upIssues(listed: Expectations): Record<string, number[]> {
  return Object.fromEntries(
    Object.keys(listed)
      .sort()
      .map((cell) => [cell, issues(listed, cell, UP_TITLE)]),
  );
}

function onEdge(environment: "aws" | "aws.floci", edge: string): Expectations {
  process.env[EDGE_ENV] = edge;
  return expectationsFor(environment);
}

describe("the gap list", () => {
  it("resolves on every environment and edge without a dead block", () => {
    for (const environment of ["dev", "vps", "vps.incus"] as const) {
      assert.doesNotThrow(() => expectationsFor(environment), environment);
    }
    for (const environment of ["aws", "aws.floci"] as const) {
      for (const edge of EDGES) {
        assert.doesNotThrow(() => onEdge(environment, edge), `${environment} on ${edge}`);
      }
    }
  });

  it("names every gap by a distinct slug and a reason", () => {
    const ids = gaps.map((gap) => gap.id);
    assert.equal(new Set(ids).size, ids.length);
    for (const gap of gaps) {
      assert.match(gap.id, /^[a-z0-9-]+$/, gap.id);
      assert.ok(gap.reason.length > 0, gap.id);
    }
  });

  it("lists the same real-world up on every edge for the cells that fail everywhere", () => {
    const everywhere: Record<string, number[]> = {
      "express/web": [911],
      "hono/web": [911],
      "fastify/web": [911],
      "next/web": [849],
      "workspace/next": [907],
      "workspace/express": [907],
      "workspace/hono": [907],
      "with-sst/web": [857],
      "with-pulumi/web": [856],
    };
    for (const edge of EDGES) {
      const listed = onEdge("aws", edge);
      for (const [cell, expected] of Object.entries(everywhere)) {
        assert.deepEqual(
          listed[cell],
          { [UP_TITLE]: listed[cell]?.[UP_TITLE] },
          `${cell} on ${edge}`,
        );
        assert.deepEqual(issues(listed, cell, UP_TITLE), expected, `${cell} on ${edge}`);
      }
    }
  });

  it("lists real-world hello-next and the with-transforms link rows on api-gateway alone", () => {
    const listed = onEdge("aws", "api-gateway");
    assert.deepEqual(issues(listed, "hello-next/web", UP_TITLE), [906]);
    assert.equal(listed["hello-express/web"], undefined);
    assert.deepEqual(
      Object.fromEntries(
        Object.entries(listed["with-transforms/web"] ?? {}).map(([title, gaps]) => [
          title,
          gaps.map((gap) => gap.issue),
        ]),
      ),
      {
        "GET /api/link/query answers ok after a select through the link": [925],
        "redeploy · GET /api/link/query answers ok after a select through the link": [925],
        "rollback · GET /api/link/query answers ok after a select through the link": [925],
        "redeploy · GET /api/link answers with what it resolved and the greeting it deployed with":
          [926],
      },
    );
  });

  it("lists every remaining real-world cell at up under the edge's own issue", () => {
    for (const [edge, issue] of [
      ["cloudfront", 923],
      ["cloudflare", 922],
    ] as const) {
      const listed = onEdge("aws", edge);
      for (const cell of ["hello-express/web", "hello-next/web", "with-transforms/web"]) {
        assert.deepEqual(Object.keys(listed[cell] ?? {}), [UP_TITLE], `${cell} on ${edge}`);
        assert.deepEqual(issues(listed, cell, UP_TITLE), [issue], `${cell} on ${edge}`);
      }
    }
  });

  it("lists no contract title under a listed up on real aws: the up covers the cell behind it", () => {
    for (const edge of EDGES) {
      for (const [name, cell] of Object.entries(onEdge("aws", edge))) {
        if (!(UP_TITLE in cell)) {
          continue;
        }
        assert.deepEqual(Object.keys(cell), [UP_TITLE], `${name} on ${edge}`);
      }
    }
  });

  it("lists every contract title on floci api-gateway, and up under the master-secret issue", () => {
    const listed = onEdge("aws.floci", "api-gateway");
    assert.deepEqual(upIssues(listed), {
      "express/web": [884],
      "fastify/web": [884],
      "hello-express/web": [],
      "hello-next/web": [906],
      "hono/web": [884],
      "next/web": [849],
      "with-pulumi/web": [856],
      "with-sst/web": [857],
      "with-transforms/web": [884],
      "workspace/express": [907],
      "workspace/hono": [907],
      "workspace/next": [907],
    });
    assert.deepEqual(issues(listed, "express/web", HEALTH), [854]);
    assert.deepEqual(issues(listed, "express/web", contractTitle("redeploy", HEALTH)), [854]);
    assert.deepEqual(issues(listed, "express/web", STREAM), [851]);
    assert.deepEqual(issues(listed, "hello-express/web", HEALTH), [854]);
    for (const row of nextCacheRows) {
      const issue = row.title === EDGE_ISR_TITLE ? 899 : 854;
      for (const leg of CONTRACT_LEGS) {
        assert.deepEqual(issues(listed, "next/web", contractTitle(leg, row.title)), [issue]);
      }
    }
  });

  it("lists no leg marker, refuse or publish title on floci, on any edge", () => {
    const publish =
      "publish · ocel link ls lists both records with their name, type, source and owner";
    for (const edge of EDGES) {
      const listed = onEdge("aws.floci", edge);
      for (const cell of Object.values(listed)) {
        assert.ok(!("redeploy" in cell) && !("rollback" in cell) && !(DESTROY_TITLE in cell), edge);
        assert.ok(!("refuse" in cell) && !(publish in cell), edge);
      }
    }
  });

  it("lists up alone on floci cloudfront and cloudflare, ladders under their own issue", () => {
    for (const [edge, issue] of [
      ["cloudfront", 852],
      ["cloudflare", 904],
    ] as const) {
      const listed = onEdge("aws.floci", edge);
      for (const [name, cell] of Object.entries(listed)) {
        assert.deepEqual(Object.keys(cell), [UP_TITLE], `${name} on ${edge}`);
      }
      assert.deepEqual(issues(listed, "express/web", UP_TITLE), [issue], edge);
      assert.deepEqual(issues(listed, "with-sst/web", UP_TITLE), [857], edge);
      assert.deepEqual(issues(listed, "with-pulumi/web", UP_TITLE), [856], edge);
    }
  });

  it("lists dev at up, destroy and the upload row, with the cache rows behind next alone", () => {
    const listed = expectationsFor("dev");
    for (const [name, cell] of Object.entries(listed)) {
      assert.deepEqual(issues(listed, name, UP_TITLE), [881], name);
      assert.deepEqual(issues(listed, name, DESTROY_TITLE), [877], name);
      assert.ok(!(contractTitle("redeploy", UPLOAD) in cell), name);
    }
    assert.deepEqual(issues(listed, "express/web", UPLOAD), [882]);
    assert.deepEqual(issues(listed, "hello-express/web", UPLOAD), []);
    assert.deepEqual(issues(listed, "next/web", nextCacheRows[0]?.title ?? ""), [898]);
    assert.deepEqual(issues(listed, "workspace/next", nextCacheRows[0]?.title ?? ""), []);
  });

  it("lists vps and vps.incus alike: composites at up, workspace at up, next-cache behind next", () => {
    const vps = expectationsFor("vps");
    assert.deepEqual(vps, expectationsFor("vps.incus"));
    assert.deepEqual(upIssues(vps), {
      "express/web": [918],
      "fastify/web": [918],
      "hono/web": [918],
      "next/web": [918],
      "workspace/express": [907],
      "workspace/hono": [907],
      "workspace/next": [907],
    });
    for (const leg of CONTRACT_LEGS) {
      assert.deepEqual(
        issues(vps, "next/web", contractTitle(leg, nextCacheRows[0]?.title ?? "")),
        [900],
      );
    }
    assert.equal(vps["hello-express/web"], undefined);
  });
});

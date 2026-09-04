import assert from "node:assert/strict";
import { describe, it } from "vitest";
import { EDGE_ISR_TITLE, nextCacheRows } from "../nextCache";
import { contractTitle, DESTROY_TITLE, planTests, UP_TITLE } from "../plan";
import { type Edge, EDGES, specForTarget } from "../spec";
import { targetNamed } from "../targets";
import { gaps } from "./gaps";
import { CONTRACT_LEGS, type Expectations, expectationsFor } from "./index";

const HEALTH = "GET /health answers with the app name";
const STREAM = "GET /api/probes/stream streams its chunks in order to the sentinel";
const UPLOAD = "the upload protocol stores a document and /api/documents lists it";

const AWS_PLAN = planTests(specForTarget("aws"), ["up"], targetNamed("aws"));

const CONTAINER_CELLS = [
  ...new Set(AWS_PLAN.filter((row) => row.variant.compute === "container").map((row) => row.cell)),
];

function on(edge: Edge, cell: string): string {
  return edge === EDGES[0] ? cell : cell.replace("/", `-${edge}/`);
}

function cellsOn(edge: Edge): Set<string> {
  return new Set(AWS_PLAN.filter((row) => row.variant.edge === edge).map((row) => row.cell));
}

function servingOn(edge: Edge): string[] {
  return [
    ...new Set(
      AWS_PLAN.filter(
        (row) => row.variant.edge === edge && row.variant.compute === "serverless",
      ).map((row) => row.cell),
    ),
  ];
}

function issues(listed: Expectations, cell: string, title: string): number[] {
  return (listed[cell]?.[title] ?? []).map((gap) => gap.issue ?? Number.NaN);
}

function upIssues(listed: Expectations, over: string[]): Record<string, number[]> {
  return Object.fromEntries(
    Object.keys(listed)
      .filter((cell) => over.includes(cell))
      .sort()
      .map((cell) => [cell, issues(listed, cell, UP_TITLE)]),
  );
}

function forEdge(edge: Edge, everywhere: Record<string, number[]>): Record<string, number[]> {
  return Object.fromEntries(
    Object.entries(everywhere).map(([cell, listed]) => [on(edge, cell), listed]),
  );
}

describe("the gap list", () => {
  it("resolves on every environment without a dead block", () => {
    for (const environment of ["dev", "vps", "vps.incus", "aws", "aws.floci"] as const) {
      assert.doesNotThrow(() => expectationsFor(environment), environment);
    }
  });

  it("lists the container gap at up on every aws container cell, and nowhere else", () => {
    assert.ok(CONTAINER_CELLS.includes("express-container/web"));
    assert.ok(CONTAINER_CELLS.includes("express-api-gateway-container/web"));
    assert.ok(CONTAINER_CELLS.includes("express-hello-cloudflare-container/web"));
    assert.ok(CONTAINER_CELLS.includes("with-transforms-cloudflare-container/web"));
    for (const environment of ["aws", "aws.floci"] as const) {
      const listed = expectationsFor(environment);
      for (const cell of CONTAINER_CELLS) {
        assert.deepEqual(issues(listed, cell, UP_TITLE), [937], `${cell} on ${environment}`);
      }
      for (const [cell, titles] of Object.entries(listed)) {
        if (CONTAINER_CELLS.includes(cell)) {
          continue;
        }
        for (const [title, rows] of Object.entries(titles)) {
          assert.ok(
            !rows.some((row) => row.id === "aws-container-unimplemented"),
            `${cell} ${title} on ${environment}`,
          );
        }
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
      "workspace/next": [849],
      "workspace/express": [849],
      "with-sst/web": [857],
      "with-pulumi/web": [856],
    };
    const listed = expectationsFor("aws");
    for (const edge of EDGES) {
      for (const [cell, expected] of Object.entries(forEdge(edge, everywhere))) {
        assert.deepEqual(listed[cell], { [UP_TITLE]: listed[cell]?.[UP_TITLE] }, cell);
        assert.deepEqual(issues(listed, cell, UP_TITLE), expected, cell);
      }
    }
  });

  it("lists a real-world hello next and the with-transforms link rows on api-gateway alone", () => {
    const listed = expectationsFor("aws");
    for (const cell of ["next-hello/web", "workspace-hello/next", "workspace-hello/express"]) {
      assert.deepEqual(issues(listed, on("api-gateway", cell), UP_TITLE), [906], cell);
    }
    assert.equal(listed["express-hello-api-gateway/web"], undefined);
    assert.deepEqual(
      Object.fromEntries(
        Object.entries(listed["with-transforms-api-gateway/web"] ?? {}).map(([title, listing]) => [
          title,
          listing.map((gap) => gap.issue),
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
    const listed = expectationsFor("aws");
    for (const [edge, issue] of [
      ["cloudfront", 923],
      ["cloudflare", 922],
    ] as const) {
      for (const cell of [
        "express-hello/web",
        "hono-hello/web",
        "fastify-hello/web",
        "next-hello/web",
        "workspace-hello/next",
        "workspace-hello/express",
        "with-transforms/web",
      ]) {
        const named = on(edge, cell);
        assert.deepEqual(Object.keys(listed[named] ?? {}), [UP_TITLE], named);
        assert.deepEqual(issues(listed, named, UP_TITLE), [issue], named);
      }
    }
  });

  it("lists no contract title under a listed up on real aws: the up covers the cell behind it", () => {
    for (const [name, cell] of Object.entries(expectationsFor("aws"))) {
      if (!(UP_TITLE in cell)) {
        continue;
      }
      assert.deepEqual(Object.keys(cell), [UP_TITLE], name);
    }
  });

  it("lists every contract title on floci api-gateway, and up under the master-secret issue", () => {
    const listed = expectationsFor("aws.floci");
    assert.deepEqual(
      upIssues(listed, servingOn("api-gateway")),
      forEdge("api-gateway", {
        "express/web": [884],
        "express-hello/web": [],
        "fastify/web": [884],
        "fastify-hello/web": [],
        "hono/web": [884],
        "hono-hello/web": [],
        "next-hello/web": [906],
        "workspace-hello/express": [906],
        "workspace-hello/next": [906],
        "next/web": [849],
        "with-pulumi/web": [856],
        "with-sst/web": [857],
        "with-transforms/web": [884],
        "workspace/express": [849],
        "workspace/next": [849],
      }),
    );
    const express = on("api-gateway", "express/web");
    assert.deepEqual(issues(listed, express, HEALTH), [854]);
    assert.deepEqual(issues(listed, express, contractTitle("redeploy", HEALTH)), [854]);
    assert.deepEqual(issues(listed, express, STREAM), [851]);
    assert.deepEqual(issues(listed, on("api-gateway", "express-hello/web"), HEALTH), [854]);
    for (const row of nextCacheRows) {
      const issue = row.title === EDGE_ISR_TITLE ? 899 : 854;
      for (const leg of CONTRACT_LEGS) {
        assert.deepEqual(
          issues(listed, on("api-gateway", "next/web"), contractTitle(leg, row.title)),
          [issue],
        );
      }
    }
  });

  it("lists no leg marker, refuse or publish title on floci, on any edge", () => {
    const publish =
      "publish · ocel link ls lists both records with their name, type, source and owner";
    for (const [name, cell] of Object.entries(expectationsFor("aws.floci"))) {
      assert.ok(!("redeploy" in cell) && !("rollback" in cell) && !(DESTROY_TITLE in cell), name);
      assert.ok(!("refuse" in cell) && !(publish in cell), name);
    }
  });

  it("lists up alone on floci cloudfront and cloudflare, ladders under their own issue", () => {
    const listed = expectationsFor("aws.floci");
    for (const [edge, issue] of [
      ["cloudfront", 852],
      ["cloudflare", 904],
    ] as const) {
      const mine = cellsOn(edge);
      for (const [name, cell] of Object.entries(listed)) {
        if (!mine.has(name)) {
          continue;
        }
        assert.deepEqual(Object.keys(cell), [UP_TITLE], name);
      }
      assert.deepEqual(issues(listed, on(edge, "express/web"), UP_TITLE), [issue], edge);
      assert.deepEqual(issues(listed, on(edge, "with-sst/web"), UP_TITLE), [857], edge);
      assert.deepEqual(issues(listed, on(edge, "with-pulumi/web"), UP_TITLE), [856], edge);
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
    assert.deepEqual(issues(listed, "express-hello/web", UPLOAD), []);
    assert.deepEqual(issues(listed, "next/web", nextCacheRows[0]?.title ?? ""), [898]);
    assert.deepEqual(issues(listed, "workspace/next", nextCacheRows[0]?.title ?? ""), []);
  });

  it("lists vps and vps.incus alike: composites at up, workspace at up, next-cache behind next", () => {
    const vps = expectationsFor("vps");
    assert.deepEqual(vps, expectationsFor("vps.incus"));
    assert.deepEqual(upIssues(vps, Object.keys(vps)), {
      "express/web": [918],
      "fastify/web": [918],
      "hono/web": [918],
      "next/web": [918],
      "workspace/express": [918],
      "workspace/next": [918],
    });
    for (const leg of CONTRACT_LEGS) {
      assert.deepEqual(
        issues(vps, "next/web", contractTitle(leg, nextCacheRows[0]?.title ?? "")),
        [900],
      );
    }
    assert.equal(vps["express-hello/web"], undefined);
  });
});

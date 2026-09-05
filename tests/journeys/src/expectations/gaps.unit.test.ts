import assert from "node:assert/strict";
import { describe, it } from "bun:test";
import { EDGE_ISR_TITLE, nextCacheRows } from "../nextCache";
import { contractTitle, DESTROY_TITLE, planTests, UP_TITLE } from "../plan";
import { cellsOf, specForTarget } from "../spec";
import { gaps } from "./gaps";
import {
  CONTRACT_LEGS,
  ENVIRONMENTS,
  type ExpectationEnvironment,
  type Expectations,
  expectationsFor,
  skippedOn,
  targetOfEnvironment,
} from "./index";

const HEALTH = "GET /health answers with the app name";
const STREAM = "GET /api/probes/stream streams its chunks in order to the sentinel";
const UPLOAD = "the upload protocol stores a document and /api/documents lists it";

const AWS_PLAN = planTests(
  specForTarget("aws").flatMap((example) => cellsOf(example, "aws")),
  ["up"],
);

function cellsOfVariant(variant: string): string[] {
  return [...new Set(AWS_PLAN.filter((row) => row.variant === variant).map((row) => row.cell))];
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

function on(variant: string, cell: string): string {
  return variant === "base" ? cell : cell.replace("/", `-${variant}/`);
}

function alive(environment: ExpectationEnvironment): string[] {
  const target = targetOfEnvironment(environment);
  const skipped = skippedOn(environment);
  return specForTarget(target)
    .flatMap((example) => cellsOf(example, target))
    .map((cell) => cell.name)
    .filter((name) => skipped[name] === undefined);
}

describe("the gap list", () => {
  it("resolves on every environment without a dead block", () => {
    for (const environment of ENVIRONMENTS) {
      assert.doesNotThrow(() => expectationsFor(environment), environment);
    }
  });

  it("lists the container gap at up on every aws container cell, and nowhere else", () => {
    const containers = cellsOfVariant("container");
    assert.ok(containers.includes("express-container/web"));
    assert.ok(containers.includes("with-transforms-container/web"));
    for (const environment of ["aws", "aws.floci"] as const) {
      const listed = expectationsFor(environment);
      for (const cell of containers) {
        assert.deepEqual(issues(listed, cell, UP_TITLE), [937], `${cell} on ${environment}`);
      }
      for (const [cell, titles] of Object.entries(listed)) {
        if (containers.includes(cell)) {
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

  it("lists the same real-world up on every serverless edge for the cells that fail everywhere", () => {
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
    for (const variant of ["base", "api-gateway", "cloudflare"]) {
      for (const [cell, expected] of Object.entries(everywhere)) {
        const named = on(variant, cell);
        assert.deepEqual(listed[named], { [UP_TITLE]: listed[named]?.[UP_TITLE] }, named);
        assert.deepEqual(issues(listed, named, UP_TITLE), expected, named);
      }
    }
  });

  it("lists a real-world hello next behind api-gateway, and the with-transforms link rows", () => {
    const listed = expectationsFor("aws");
    for (const cell of ["next/web", "workspace/next", "workspace/express"]) {
      assert.deepEqual(issues(listed, on("hello-api-gateway", cell), UP_TITLE), [906], cell);
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

  it("lists every hello cell and the with-transforms edges at up under the edge's own issue", () => {
    const listed = expectationsFor("aws");
    for (const cell of [
      "express-hello/web",
      "hono-hello/web",
      "fastify-hello/web",
      "next-hello/web",
      "workspace-hello/next",
      "workspace-hello/express",
      "with-transforms/web",
    ]) {
      assert.deepEqual(Object.keys(listed[cell] ?? {}), [UP_TITLE], cell);
      assert.deepEqual(issues(listed, cell, UP_TITLE), [923], cell);
    }
    assert.deepEqual(Object.keys(listed["with-transforms-cloudflare/web"] ?? {}), [UP_TITLE]);
    assert.deepEqual(issues(listed, "with-transforms-cloudflare/web", UP_TITLE), [922]);
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
      upIssues(listed, [...cellsOfVariant("api-gateway"), ...cellsOfVariant("hello-api-gateway")]),
      {
        "express-api-gateway/web": [884],
        "express-hello-api-gateway/web": [],
        "fastify-api-gateway/web": [884],
        "fastify-hello-api-gateway/web": [],
        "hono-api-gateway/web": [884],
        "hono-hello-api-gateway/web": [],
        "next-hello-api-gateway/web": [906],
        "workspace-hello-api-gateway/express": [906],
        "workspace-hello-api-gateway/next": [906],
        "next-api-gateway/web": [849],
        "with-pulumi-api-gateway/web": [856],
        "with-sst-api-gateway/web": [857],
        "with-transforms-api-gateway/web": [884],
        "workspace-api-gateway/express": [849],
        "workspace-api-gateway/next": [849],
      },
    );
    const express = "express-api-gateway/web";
    assert.deepEqual(issues(listed, express, HEALTH), [854]);
    assert.deepEqual(issues(listed, express, contractTitle("redeploy", HEALTH)), [854]);
    assert.deepEqual(issues(listed, express, STREAM), [851]);
    assert.deepEqual(issues(listed, "express-hello-api-gateway/web", HEALTH), [854]);
    for (const row of nextCacheRows) {
      const issue = row.title === EDGE_ISR_TITLE ? 899 : 854;
      for (const leg of CONTRACT_LEGS) {
        assert.deepEqual(
          issues(listed, "next-api-gateway/web", contractTitle(leg, row.title)),
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
    for (const [variant, issue] of [
      ["base", 852],
      ["hello", 852],
      ["cloudflare", 904],
    ] as const) {
      const mine = new Set(cellsOfVariant(variant));
      for (const [name, cell] of Object.entries(listed)) {
        if (!mine.has(name)) {
          continue;
        }
        assert.deepEqual(Object.keys(cell), [UP_TITLE], name);
      }
      assert.deepEqual(issues(listed, on(variant, "express/web"), UP_TITLE), [issue], variant);
    }
    for (const variant of ["base", "cloudflare"]) {
      assert.deepEqual(issues(listed, on(variant, "with-sst/web"), UP_TITLE), [857], variant);
      assert.deepEqual(issues(listed, on(variant, "with-pulumi/web"), UP_TITLE), [856], variant);
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

  it("skips every cell that is listed dead at up, and leaves the live ones to run", () => {
    assert.deepEqual(alive("aws"), [
      "express-hello-api-gateway",
      "hono-hello-api-gateway",
      "fastify-hello-api-gateway",
      "with-transforms-api-gateway",
    ]);
    assert.deepEqual(alive("aws.floci"), [
      "express-hello-api-gateway",
      "hono-hello-api-gateway",
      "fastify-hello-api-gateway",
    ]);
    assert.deepEqual(alive("dev"), []);
    assert.deepEqual(alive("vps"), [
      "express-hello",
      "hono-hello",
      "fastify-hello",
      "next-hello",
      "workspace-hello",
    ]);
    assert.deepEqual(alive("vps.incus"), alive("vps"));
  });

  it("skips a cell only under a gap that lists its up", () => {
    for (const environment of ENVIRONMENTS) {
      const listed = expectationsFor(environment);
      for (const [cell, why] of Object.entries(skippedOn(environment))) {
        const ups = Object.entries(listed)
          .filter(([name]) => name.split("/")[0] === cell)
          .flatMap(([, titles]) => titles[UP_TITLE] ?? []);
        for (const gap of why) {
          assert.ok(ups.some((one) => one.id === gap.id), `${cell} on ${environment} via ${gap.id}`);
        }
      }
    }
  });

  it("skips no cell for a gap that is red past up, where the cell still stands to observe", () => {
    for (const gap of gaps) {
      for (const block of gap.affects) {
        if (block.skip) {
          assert.deepEqual(block.tests, [UP_TITLE], gap.id);
        }
      }
    }
  });
});

import assert from "node:assert/strict";
import { describe, it } from "bun:test";
import { contractTitle, DESTROY_TITLE, planTests, UP_TITLE } from "../plan";
import { EDGE_ISR_TITLE, nextCacheRows } from "../rows";
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
  specForTarget("aws").flatMap((fixture) => cellsOf(fixture, "aws")),
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
  const cut = cell.lastIndexOf("/");
  return variant === "base"
    ? cell
    : `${cell.slice(0, cut)}-${variant}${cell.slice(cut)}`;
}

function alive(environment: ExpectationEnvironment): string[] {
  const target = targetOfEnvironment(environment);
  const skipped = skippedOn(environment);
  return specForTarget(target)
    .flatMap((fixture) => cellsOf(fixture, target))
    .map((cell) => cell.name)
    .filter((name) => skipped[name] === undefined);
}

describe("the gap list", () => {
  it("resolves on every environment without a dead block", () => {
    for (const environment of ENVIRONMENTS) {
      assert.doesNotThrow(() => expectationsFor(environment), environment);
    }
  });

  it("names every cell it lists by the concern the fixture belongs to", () => {
    for (const environment of ENVIRONMENTS) {
      for (const cell of Object.keys(expectationsFor(environment))) {
        assert.match(cell, /^(deploy|sdk)\//, `${cell} on ${environment}`);
      }
      for (const cell of Object.keys(skippedOn(environment))) {
        assert.match(cell, /^(deploy|sdk)\//, `${cell} on ${environment}`);
      }
    }
  });

  it("lists the container gap at up on every aws container cell, and nowhere else", () => {
    const containers = cellsOfVariant("container");
    assert.ok(containers.includes("deploy/node-container/web"));
    assert.ok(containers.includes("sdk/with-transforms-container/web"));
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

  it("lists the same real-world up on every serverless edge for the sdk cells that fail everywhere", () => {
    const everywhere: Record<string, number[]> = {
      "sdk/node/web": [911],
      "sdk/next/web": [849],
      "sdk/workspace/next": [849],
      "sdk/workspace/express": [849],
      "sdk/with-sst/web": [857],
      "sdk/with-pulumi/web": [856],
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

  it("lists a real-world deploy next behind api-gateway, and the with-transforms link rows", () => {
    const listed = expectationsFor("aws");
    for (const cell of ["deploy/next/web", "deploy/workspace/next", "deploy/workspace/express"]) {
      assert.deepEqual(issues(listed, on("api-gateway", cell), UP_TITLE), [906], cell);
    }
    assert.equal(listed["deploy/node-api-gateway/web"], undefined);
    assert.deepEqual(
      Object.fromEntries(
        Object.entries(listed["sdk/with-transforms-api-gateway/web"] ?? {}).map(
          ([title, listing]) => [title, listing.map((gap) => gap.issue)],
        ),
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

  it("lists every deploy cell and the with-transforms edges at up under the edge's own issue", () => {
    const listed = expectationsFor("aws");
    for (const cell of [
      "deploy/node/web",
      "deploy/next/web",
      "deploy/workspace/next",
      "deploy/workspace/express",
      "sdk/with-transforms/web",
    ]) {
      assert.deepEqual(Object.keys(listed[cell] ?? {}), [UP_TITLE], cell);
      assert.deepEqual(issues(listed, cell, UP_TITLE), [923], cell);
    }
    assert.equal(listed["sdk/with-transforms-cloudflare/web"], undefined);
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
    assert.deepEqual(upIssues(listed, cellsOfVariant("api-gateway")), {
      "deploy/node-api-gateway/web": [],
      "deploy/next-api-gateway/web": [906],
      "deploy/workspace-api-gateway/express": [906],
      "deploy/workspace-api-gateway/next": [906],
      "sdk/node-api-gateway/web": [884],
      "sdk/next-api-gateway/web": [849],
      "sdk/with-pulumi-api-gateway/web": [856],
      "sdk/with-sst-api-gateway/web": [857],
      "sdk/with-transforms-api-gateway/web": [884],
      "sdk/workspace-api-gateway/express": [849],
      "sdk/workspace-api-gateway/next": [849],
    });
    const node = "sdk/node-api-gateway/web";
    assert.deepEqual(issues(listed, node, HEALTH), [854]);
    assert.deepEqual(issues(listed, node, contractTitle("redeploy", HEALTH)), [854]);
    assert.deepEqual(issues(listed, node, STREAM), [851]);
    assert.deepEqual(issues(listed, "deploy/node-api-gateway/web", HEALTH), [854]);
    for (const row of nextCacheRows) {
      const issue = row.title === EDGE_ISR_TITLE ? 899 : 854;
      for (const leg of CONTRACT_LEGS) {
        for (const cell of ["sdk/next-api-gateway/web", "deploy/next-api-gateway/web"]) {
          assert.deepEqual(issues(listed, cell, contractTitle(leg, row.title)), [issue], cell);
        }
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
      ["cloudflare", 904],
    ] as const) {
      const mine = new Set(cellsOfVariant(variant));
      for (const [name, cell] of Object.entries(listed)) {
        if (!mine.has(name)) {
          continue;
        }
        assert.deepEqual(Object.keys(cell), [UP_TITLE], name);
      }
      assert.deepEqual(
        issues(listed, on(variant, "deploy/node/web"), UP_TITLE),
        [issue],
        variant,
      );
    }
    for (const variant of ["base", "cloudflare"]) {
      assert.deepEqual(issues(listed, on(variant, "sdk/with-sst/web"), UP_TITLE), [857], variant);
      assert.deepEqual(issues(listed, on(variant, "sdk/with-pulumi/web"), UP_TITLE), [856], variant);
    }
  });

  it("lists dev at destroy on both buckets, and up and the upload row on the sdk one alone", () => {
    const listed = expectationsFor("dev");
    for (const [name, cell] of Object.entries(listed)) {
      assert.deepEqual(issues(listed, name, UP_TITLE), name.startsWith("sdk/") ? [881] : [], name);
      assert.deepEqual(issues(listed, name, DESTROY_TITLE), [877], name);
      assert.ok(!(contractTitle("redeploy", UPLOAD) in cell), name);
    }
    assert.deepEqual(issues(listed, "sdk/node/web", UPLOAD), [882]);
    assert.deepEqual(issues(listed, "deploy/node/web", UPLOAD), []);
    for (const cell of ["sdk/next/web", "deploy/next/web"]) {
      assert.deepEqual(issues(listed, cell, nextCacheRows[0]?.title ?? ""), [898], cell);
    }
    assert.deepEqual(issues(listed, "sdk/workspace/next", nextCacheRows[0]?.title ?? ""), []);
  });

  it("lists vps and vps.incus alike: the sdk bucket at up, next-cache behind either next", () => {
    const vps = expectationsFor("vps");
    assert.deepEqual(vps, expectationsFor("vps.incus"));
    assert.deepEqual(upIssues(vps, Object.keys(vps)), {
      "deploy/next/web": [],
      "sdk/node/web": [918],
      "sdk/next/web": [918],
      "sdk/workspace/express": [918],
      "sdk/workspace/next": [918],
    });
    for (const leg of CONTRACT_LEGS) {
      for (const cell of ["sdk/next/web", "deploy/next/web"]) {
        assert.deepEqual(
          issues(vps, cell, contractTitle(leg, nextCacheRows[0]?.title ?? "")),
          [900],
          cell,
        );
      }
    }
    assert.equal(vps["deploy/node/web"], undefined);
  });

  it("skips every cell that is listed dead at up, and leaves the live ones to run", () => {
    assert.deepEqual(alive("aws"), [
      "deploy/node-api-gateway",
      "deploy/node-cloudflare",
      "deploy/next-cloudflare",
      "deploy/workspace-cloudflare",
      "sdk/with-transforms-api-gateway",
      "sdk/with-transforms-cloudflare",
    ]);
    assert.deepEqual(alive("aws.floci"), [
      "deploy/node-api-gateway",
    ]);
    assert.deepEqual(alive("dev"), [
      "deploy/node",
      "deploy/next",
      "deploy/workspace",
    ]);
    assert.deepEqual(alive("vps"), [
      "deploy/node",
      "deploy/next",
      "deploy/workspace",
    ]);
    assert.deepEqual(alive("vps.incus"), alive("vps"));
  });

  it("skips a cell only under a gap that lists its up", () => {
    for (const environment of ENVIRONMENTS) {
      const listed = expectationsFor(environment);
      for (const [cell, why] of Object.entries(skippedOn(environment))) {
        const ups = Object.entries(listed)
          .filter(([name]) => name.slice(0, name.lastIndexOf("/")) === cell)
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
